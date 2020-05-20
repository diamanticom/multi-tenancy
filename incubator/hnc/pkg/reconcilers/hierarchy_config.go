/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package reconcilers

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	api "sigs.k8s.io/multi-tenancy/incubator/hnc/api/v1alpha1"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/pkg/config"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/pkg/forest"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/pkg/metadata"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/pkg/stats"
)

// HierarchyConfigReconciler is responsible for determining the forest structure from the Hierarchy CRs,
// as well as ensuring all objects in the forest are propagated correctly when the hierarchy
// changes. It can also set the status of the Hierarchy CRs, as well as (in rare cases) override
// part of its spec (i.e., if a parent namespace no longer exists).
type HierarchyConfigReconciler struct {
	client.Client
	Log logr.Logger

	// Forest is the in-memory data structure that is shared with all other reconcilers.
	// HierarchyConfigReconciler is responsible for keeping it up-to-date, but the other reconcilers
	// use it to determine how to propagate objects.
	Forest *forest.Forest

	// Affected is a channel of event.GenericEvent (see "Watching Channels" in
	// https://book-v1.book.kubebuilder.io/beyond_basics/controller_watches.html) that is used to
	// enqueue additional namespaces that need updating.
	Affected chan event.GenericEvent

	// reconcileID is used purely to set the "rid" field in the log, so we can tell which log messages
	// were part of the same reconciliation attempt, even if multiple are running parallel (or it's
	// simply hard to tell when one ends and another begins).
	reconcileID int32

	sar *AnchorReconciler
}

// +kubebuilder:rbac:groups=hnc.x-k8s.io,resources=hierarchies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hnc.x-k8s.io,resources=hierarchies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;update;patch

// Reconcile sets up some basic variables and then calls the business logic.
func (r *HierarchyConfigReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	if config.EX[req.Namespace] {
		return ctrl.Result{}, nil
	}

	stats.StartHierConfigReconcile()
	defer stats.StopHierConfigReconcile()

	ctx := context.Background()
	ns := req.NamespacedName.Namespace

	rid := (int)(atomic.AddInt32(&r.reconcileID, 1))
	log := r.Log.WithValues("ns", ns, "rid", rid)

	return ctrl.Result{}, r.reconcile(ctx, log, ns)
}

func (r *HierarchyConfigReconciler) reconcile(ctx context.Context, log logr.Logger, nm string) error {
	nsInst, err := r.getNamespace(ctx, nm)
	if err != nil {
		if errors.IsNotFound(err) {
			// The namespace doesn't exist or is purged. Update the forest and exit.
			// (There must be no HC instance and we cannot create one.)
			r.onMissingNamespace(log, nm)
			return nil
		}
		return err
	}
	// Get singleton from apiserver. If it doesn't exist, initialize one.
	inst, err := r.getSingleton(ctx, nm)
	if err != nil {
		return err
	}
	// Get a list of subnamespace anchors from apiserver.
	anms, err := r.getAnchorNames(ctx, nm)
	if err != nil {
		return err
	}

	origHC := inst.DeepCopy()
	origNS := nsInst.DeepCopy()

	r.updateFinalizers(ctx, log, inst, nsInst, anms)

	// Sync the Hierarchy singleton with the in-memory forest.
	r.syncWithForest(log, nsInst, inst, anms)

	// Write back if anything's changed. Early-exit if we just write back exactly what we had.
	if updated, err := r.writeInstances(ctx, log, origHC, inst, origNS, nsInst); !updated || err != nil {
		return err
	}

	// Update all the objects in this namespace. We have to do this at least *after* the tree is
	// updated, because if we don't, we could incorrectly think we've propagated the wrong objects
	// from our ancestors, or are propagating the wrong objects to our descendants.
	//
	// NB: if writeInstance didn't actually write anything - that is, if the hierarchy didn't change -
	// this update is skipped. Otherwise, we can get into infinite loops because both objects and
	// hierarchy reconcilers are enqueuing too freely. TODO: only call updateObjects when we make the
	// *kind* of changes that *should* cause objects to be updated (eg add/remove critical conditions,
	// change subtree parents, etc).
	return r.updateObjects(ctx, log, nm)
}

func (r *HierarchyConfigReconciler) onMissingNamespace(log logr.Logger, nm string) {
	r.Forest.Lock()
	defer r.Forest.Unlock()
	ns := r.Forest.Get(nm)

	if ns.Exists() {
		r.enqueueAffected(log, "relative of deleted namespace", ns.RelativesNames()...)
		ns.UnsetExists()
		log.Info("Removed namespace")
	}
}

func (r *HierarchyConfigReconciler) updateFinalizers(ctx context.Context, log logr.Logger, inst *api.HierarchyConfiguration, nsInst *corev1.Namespace, anms []string) {
	// No-one should put a finalizer on a hierarchy config except us. See
	// https://github.com/kubernetes-sigs/multi-tenancy/issues/623 as we try to enforce that.
	switch {
	case len(anms) == 0:
		// There are no subnamespaces in this namespace. The HC instance can be safely deleted anytime.
		if len(inst.ObjectMeta.Finalizers) > 0 {
			log.Info("Removing finalizers since there are no longer any anchors in the namespace.")
		}
		inst.ObjectMeta.Finalizers = nil
	case !inst.DeletionTimestamp.IsZero() && nsInst.DeletionTimestamp.IsZero():
		// If the HC instance is being deleted but not the namespace (which means
		// it's not a cascading delete), remove the finalizers to let it go through.
		// This is the only case the finalizers can be removed even when the
		// namespace has subnamespaces. (A default HC will be recreated later.)
		log.Info("Removing finalizers to allow a single deletion of the singleton (not involved in a cascading deletion).")
		inst.ObjectMeta.Finalizers = nil
	default:
		if len(inst.ObjectMeta.Finalizers) == 0 {
			log.Info("Adding finalizers since there's at least one anchor in the namespace.")
		}
		inst.ObjectMeta.Finalizers = []string{api.FinalizerHasOwnedNamespace}
	}
}

// syncWithForest synchronizes the in-memory forest with the (in-memory) Hierarchy instance. If any
// *other* namespaces have changed, it enqueues them for later reconciliation. This method is
// guarded by the forest mutex, which means that none of the other namespaces being reconciled will
// be able to proceed until this one is finished. While the results of the reconiliation may not be
// fully written back to the apiserver yet, each namespace is reconciled in isolation (apart from
// the in-memory forest) so this is fine.
func (r *HierarchyConfigReconciler) syncWithForest(log logr.Logger, nsInst *corev1.Namespace, inst *api.HierarchyConfiguration, anms []string) {
	r.Forest.Lock()
	defer r.Forest.Unlock()
	ns := r.Forest.Get(inst.ObjectMeta.Namespace)

	// Clear locally-set conditions in the forest; we'll re-add them if they're still relevant. But
	// first, record whether there were any critical ones since if this changes, we'll need to notify
	// other namespaces.
	hadCrit := ns.HasLocalCritCondition()
	ns.ClearLocalConditions()

	// If this is a subnamespace, make sure .spec.parent is set correctly. Then sync the parent to the
	// forest, and finally notify any relatives (including the parent) that might have been waiting
	// for this namespace to be synced.
	r.syncSubnamespaceParent(log, inst, nsInst, ns)
	r.syncParent(log, inst, ns)
	r.markExisting(log, ns)

	// Sync other spec and spec-like info
	r.syncAnchors(log, ns, anms)
	ns.UpdateAllowCascadingDelete(inst.Spec.AllowCascadingDelete)

	// Sync the status
	inst.Status.Children = ns.ChildNames()
	r.syncConditions(log, inst, ns, hadCrit)

	// Sync the tree labels. This should go last since it can depend on the conditions.
	r.syncLabel(log, nsInst, ns)
}

// syncSubnamespaceParent sets the parent to the owner and updates the SubnamespaceAnchorMissing
// condition if the anchor is missing in the parent namespace according to the forest. The
// subnamespaceOf annotation is the source of truth of the ownership (e.g. being a subnamespace),
// since modifying a namespace has higher privilege than what HNC users can do.
func (r *HierarchyConfigReconciler) syncSubnamespaceParent(log logr.Logger, inst *api.HierarchyConfiguration, nsInst *corev1.Namespace, ns *forest.Namespace) {
	pnm := nsInst.Annotations[api.SubnamespaceOf]
	if pnm == "" {
		ns.IsSub = false
		return
	}
	ns.IsSub = true

	if inst.Spec.Parent != pnm {
		log.Info("The parent doesn't match the subnamespace annotation; overwriting parent", "oldParent", inst.Spec.Parent, "parent", pnm)
		inst.Spec.Parent = pnm
	}

	// Look up the Anchors in the parent namespace. Set SubnamespaceAnchorMissing condition if it's
	// not there.
	found := false
	for _, anm := range r.Forest.Get(pnm).Anchors {
		if anm == ns.Name() {
			found = true
			break
		}
	}
	if !found {
		ns.SetLocalCondition(api.SubnamespaceAnchorMissing, "The anchor is missing in the parent namespace")
	}
}

// markExisting marks the namespace as existing. If this is the first time we're reconciling this namespace,
// mark all possible relatives as being affected since they may have been waiting for this namespace.
func (r *HierarchyConfigReconciler) markExisting(log logr.Logger, ns *forest.Namespace) {
	if ns.SetExists() {
		log.Info("Reconciling new namespace")
		r.enqueueAffected(log, "relative of newly synced/created namespace", ns.RelativesNames()...)
		if ns.IsSub {
			r.enqueueAffected(log, "parent of the newly synced/created subnamespace", ns.Parent().Name())
			r.sar.enqueue(log, ns.Name(), ns.Parent().Name(), "the missing subnamespace is found")
		}
	}
}

func (r *HierarchyConfigReconciler) syncParent(log logr.Logger, inst *api.HierarchyConfiguration, ns *forest.Namespace) {
	// Sync this namespace with its current parent.
	curParent := r.Forest.Get(inst.Spec.Parent)
	if curParent != nil && !curParent.Exists() {
		log.Info("Missing parent", "parent", inst.Spec.Parent)
		ns.SetLocalCondition(api.CritParentMissing, "missing parent")
	}

	// If the parent hasn't changed, there's nothing more to do.
	oldParent := ns.Parent()
	if curParent == oldParent {
		return
	}

	// If this namespace *was* involved in a cycle, enqueue all elements in that cycle in the hopes
	// we're about to break it.
	r.enqueueAffected(log, "member of a cycle", ns.CycleNames()...)

	// Change the parent.
	ns.SetParent(curParent)

	// Finally, enqueue all other namespaces that could be directly affected. The old and new parents
	// have just gained/lost a child, while the descendants need to have their tree labels updated and
	// their objects resynced. Note that it's fine if oldParent or curParent is nil - see
	// enqueueAffected for details.
	//
	// If we've just created a cycle, all the members of that cycle will be listed as the descendants,
	// so enqueuing them will ensure that the conditions show up in all members of the cycle.
	r.enqueueAffected(log, "removed as parent", oldParent.Name())
	r.enqueueAffected(log, "set as parent", curParent.Name())
	r.enqueueAffected(log, "subtree parent has changed", ns.DescendantNames()...)
}

// syncAnchors updates the anchor list. If any anchor is created/deleted, it will enqueue
// the child to update its SubnamespaceAnchorMissing condition. A modified anchor will appear
// twice in the change list (one in deleted, one in created), both subnamespaces
// needs to be enqueued in this case.
func (r *HierarchyConfigReconciler) syncAnchors(log logr.Logger, ns *forest.Namespace, anms []string) {
	for _, changedAnchors := range ns.SetAnchors(anms) {
		r.enqueueAffected(log, "SubnamespaceAnchorMissing condition may have changed due to anchor being created/deleted", changedAnchors)
	}
}

func (r *HierarchyConfigReconciler) syncLabel(log logr.Logger, nsInst *corev1.Namespace, ns *forest.Namespace) {
	// Pre-define label depth suffix
	labelDepthSuffix := fmt.Sprintf(".tree.%s/depth", api.MetaGroup)

	// Remove all existing depth labels.
	for k := range nsInst.Labels {
		if strings.HasSuffix(k, labelDepthSuffix) {
			delete(nsInst.Labels, k)
		}
	}

	// Look for all ancestors. Stop as soon as we find a namespaces that has a critical condition in
	// the forest (note that CritAncestor is never included in the forest). This should handle orphans
	// and cycles.
	anc := ns
	depth := 0
	for anc != nil {
		l := anc.Name() + labelDepthSuffix
		metadata.SetLabel(nsInst, l, strconv.Itoa(depth))
		if anc.HasLocalCritCondition() {
			break
		}
		anc = anc.Parent()
		depth++
	}
}

func (r *HierarchyConfigReconciler) syncConditions(log logr.Logger, inst *api.HierarchyConfiguration, ns *forest.Namespace, hadCrit bool) {
	// Hierarchy changes may mean that some object conditions are no longer relevant.
	ns.ClearObsoleteConditions(log)

	// Sync critical conditions after all locally-set conditions are updated.
	r.syncCritConditions(log, ns, hadCrit)

	// Convert and pass in-memory conditions to HierarchyConfiguration object.
	inst.Status.Conditions = ns.Conditions()
	setCritAncestorCondition(log, inst, ns)
}

// syncCritConditions enqueues the children of a namespace if the existing critical conditions in the
// namespace are gone or critical conditions are newly found.
func (r *HierarchyConfigReconciler) syncCritConditions(log logr.Logger, ns *forest.Namespace, hadCrit bool) {
	// If we're in a cycle, determine that now
	if cycle := ns.CycleNames(); cycle != nil {
		msg := fmt.Sprintf("Namespace is a member of the cycle: %s", strings.Join(cycle, " <- "))
		ns.SetLocalCondition(api.CritCycle, msg)
	}

	// Early exit if there's no need to enqueue relatives.
	if hadCrit == ns.HasLocalCritCondition() {
		return
	}

	msg := "added"
	if hadCrit == true {
		msg = "removed"
	}
	log.Info("Critical conditions are " + msg)
	r.enqueueAffected(log, "descendant of a namespace with critical conditions "+msg, ns.DescendantNames()...)
}

func setCritAncestorCondition(log logr.Logger, inst *api.HierarchyConfiguration, ns *forest.Namespace) {
	if ns.HasLocalCritCondition() {
		return
	}
	ans := ns.Parent()
	for ans != nil {
		if !ans.HasLocalCritCondition() {
			ans = ans.Parent()
			continue
		}
		log.Info("Ancestor has a critical condition", "ancestor", ans.Name())
		msg := fmt.Sprintf("Propagation paused in the subtree of %s due to a critical condition", ans.Name())
		condition := api.Condition{
			Code:    api.CritAncestor,
			Msg:     msg,
			Affects: []api.AffectedObject{{Namespace: ans.Name()}},
		}
		inst.Status.Conditions = append(inst.Status.Conditions, condition)
		return
	}
}

// enqueueAffected enqueues all affected namespaces for later reconciliation. This occurs in a
// goroutine so the caller doesn't block; since the reconciler is never garbage-collected, this is
// safe.
//
// It's fine to call this function with `foo.Name()` even if `foo` is nil; it will just be ignored.
func (r *HierarchyConfigReconciler) enqueueAffected(log logr.Logger, reason string, affected ...string) {
	go func() {
		for _, nm := range affected {
			// Ignore any nil namespaces (lets callers skip a nil check)
			if nm == (*forest.Namespace)(nil).Name() {
				continue
			}
			log.Info("Enqueuing for reconcilation", "affected", nm, "reason", reason)
			// The watch handler doesn't care about anything except the metadata.
			inst := &api.HierarchyConfiguration{}
			inst.ObjectMeta.Name = api.Singleton
			inst.ObjectMeta.Namespace = nm
			r.Affected <- event.GenericEvent{Meta: inst}
		}
	}()
}

func (r *HierarchyConfigReconciler) writeInstances(ctx context.Context, log logr.Logger, oldHC, newHC *api.HierarchyConfiguration, oldNS, newNS *corev1.Namespace) (bool, error) {
	isDeletingNS := !newNS.DeletionTimestamp.IsZero()
	updated := false
	if up, err := r.writeHierarchy(ctx, log, oldHC, newHC, isDeletingNS); err != nil {
		return false, err
	} else {
		updated = updated || up
	}

	if up, err := r.writeNamespace(ctx, log, oldNS, newNS); err != nil {
		return false, err
	} else {
		updated = updated || up
	}
	return updated, nil
}

func (r *HierarchyConfigReconciler) writeHierarchy(ctx context.Context, log logr.Logger, orig, inst *api.HierarchyConfiguration, isDeletingNS bool) (bool, error) {
	if reflect.DeepEqual(orig, inst) {
		return false, nil
	}
	exists := !inst.CreationTimestamp.IsZero()
	if !exists && isDeletingNS {
		log.Info("Will not create singleton since namespace is being deleted")
		return false, nil
	}

	stats.WriteHierConfig()
	if !exists {
		log.Info("Creating singleton on apiserver", "conditions", len(inst.Status.Conditions))
		if err := r.Create(ctx, inst); err != nil {
			log.Error(err, "while creating on apiserver")
			return false, err
		}
	} else {
		log.Info("Updating singleton on apiserver", "conditions", len(inst.Status.Conditions))
		if err := r.Update(ctx, inst); err != nil {
			log.Error(err, "while updating apiserver")
			return false, err
		}
	}

	return true, nil
}

func (r *HierarchyConfigReconciler) writeNamespace(ctx context.Context, log logr.Logger, orig, inst *corev1.Namespace) (bool, error) {
	if reflect.DeepEqual(orig, inst) {
		return false, nil
	}

	// NB: HCR can't create namespaces, that's only in anchor reconciler
	stats.WriteNamespace()
	log.Info("Updating namespace on apiserver")
	if err := r.Update(ctx, inst); err != nil {
		log.Error(err, "while updating apiserver")
		return false, err
	}

	return true, nil
}

// updateObjects calls all type reconcillers in this namespace.
func (r *HierarchyConfigReconciler) updateObjects(ctx context.Context, log logr.Logger, ns string) error {
	// Use mutex to guard the read from the types list of the forest to prevent the ConfigReconciler
	// from modifying the list at the same time.
	r.Forest.Lock()
	trs := r.Forest.GetTypeSyncers()
	r.Forest.Unlock()
	for _, tr := range trs {
		if err := tr.SyncNamespace(ctx, log, ns); err != nil {
			return err
		}
	}

	return nil
}

// getSingleton returns the singleton if it exists, or creates an empty one if it doesn't.
func (r *HierarchyConfigReconciler) getSingleton(ctx context.Context, nm string) (*api.HierarchyConfiguration, error) {
	nnm := types.NamespacedName{Namespace: nm, Name: api.Singleton}
	inst := &api.HierarchyConfiguration{}
	if err := r.Get(ctx, nnm, inst); err != nil {
		if !errors.IsNotFound(err) {
			return nil, err
		}

		// It doesn't exist - initialize it to a sane initial value.
		inst.ObjectMeta.Name = api.Singleton
		inst.ObjectMeta.Namespace = nm
	}

	return inst, nil
}

// getNamespace returns the namespace if it exists, or returns an invalid, blank, unnamed one if it
// doesn't. This allows it to be trivially identified as a namespace that doesn't exist, and also
// allows us to easily modify it if we want to create it.
func (r *HierarchyConfigReconciler) getNamespace(ctx context.Context, nm string) (*corev1.Namespace, error) {
	ns := &corev1.Namespace{}
	nnm := types.NamespacedName{Name: nm}
	if err := r.Get(ctx, nnm, ns); err != nil {
		return nil, err
	}
	return ns, nil
}

// getAnchorNames returns a list of anchor names in the given namespace.
func (r *HierarchyConfigReconciler) getAnchorNames(ctx context.Context, nm string) ([]string, error) {
	var anms []string

	// List all the anchor in the namespace.
	ul := &unstructured.UnstructuredList{}
	ul.SetKind(api.AnchorKind)
	ul.SetAPIVersion(api.AnchorAPIVersion)
	if err := r.List(ctx, ul, client.InNamespace(nm)); err != nil {
		if !errors.IsNotFound(err) {
			return nil, err
		}
		return anms, nil
	}

	// Create a list of strings of the anchor names.
	for _, inst := range ul.Items {
		anms = append(anms, inst.GetName())
	}

	return anms, nil
}

func (r *HierarchyConfigReconciler) SetupWithManager(mgr ctrl.Manager, maxReconciles int) error {
	// Maps namespaces to their singletons
	nsMapFn := handler.ToRequestsFunc(
		func(a handler.MapObject) []reconcile.Request {
			return []reconcile.Request{
				{NamespacedName: types.NamespacedName{
					Name:      api.Singleton,
					Namespace: a.Meta.GetName(),
				}},
			}
		})
	// Maps a subnamespace anchor to the parent singleton.
	anchorMapFn := handler.ToRequestsFunc(
		func(a handler.MapObject) []reconcile.Request {
			return []reconcile.Request{
				{NamespacedName: types.NamespacedName{
					Name:      api.Singleton,
					Namespace: a.Meta.GetNamespace(),
				}},
			}
		})
	opts := controller.Options{
		MaxConcurrentReconciles: maxReconciles,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.HierarchyConfiguration{}).
		Watches(&source.Channel{Source: r.Affected}, &handler.EnqueueRequestForObject{}).
		Watches(&source.Kind{Type: &corev1.Namespace{}}, &handler.EnqueueRequestsFromMapFunc{ToRequests: nsMapFn}).
		Watches(&source.Kind{Type: &api.SubnamespaceAnchor{}}, &handler.EnqueueRequestsFromMapFunc{ToRequests: anchorMapFn}).
		WithOptions(opts).
		Complete(r)
}

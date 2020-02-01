/*
Copyright 2019 The Kubernetes Authors.

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

package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"gitlab.eng.diamanti.com/software/mcm.git/dmc/pkg/serde"
	"k8s.io/apimachinery/pkg/types"

	"github.com/diamanticom/multi-tenancy/tenant/clusterapi"
	tenancyv1alpha1 "github.com/diamanticom/multi-tenancy/tenant/pkg/apis/tenancy/v1alpha1"
	"gitlab.eng.diamanti.com/software/mcm.git/dmc/pkg/secret"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sync"
)

var log = logf.Log.WithName("controller")

const VaultAddress = "http://vault:8200"
const VaultKvPath = "kv"

// Keep per tenancy vault keys in this map.
//For now not using it, going forward make use of it
var doOnce sync.Once
var tenancymap map[string][]rbacv1.Subject
var tenancyMutex *sync.RWMutex
var sp_ns string = "default"

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new Tenant Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileTenant{Client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("tenant-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to Tenant
	err = c.Watch(&source.Kind{Type: &tenancyv1alpha1.Tenant{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch for changes to namespaces
	err = c.Watch(&source.Kind{Type: &corev1.Namespace{}}, &handler.EnqueueRequestForOwner{
		OwnerType: &tenancyv1alpha1.Tenant{},
	})
	if err != nil {
		return err
	}
	return nil
}

var _ reconcile.Reconciler = &ReconcileTenant{}

// ReconcileTenant reconciles a Tenant object
type ReconcileTenant struct {
	client.Client
	scheme *runtime.Scheme
}

// Create if not existing, update otherwise
func (r *ReconcileTenant) clientApply(obj runtime.Object) error {
	var err error
	if err = r.Client.Create(context.TODO(), obj); err != nil {
		if errors.IsAlreadyExists(err) {
			// Update instead of create
			err = r.Client.Update(context.TODO(), obj)
		}
	}
	return err
}

func (r *ReconcileTenant) GetAuthorizationTokenfromSecret(ns string, tenancyname string) (string, error) {

	obj := &corev1.Secret{}

	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: tenancyname, Namespace: ns}, obj)
	if err != nil {
		return "", err
	}

	token, ok := obj.Data["vault-token"]
	if !ok {
		return "", fmt.Errorf("valid token could not be found")
	}

	if len(token) > 0 {
		return string(token), nil
	}

	return "", fmt.Errorf("valid token could not be found")
}

func (r *ReconcileTenant) GetAuthorizationToken(ns string, serviceAccountName string) (string, error) {
	c := r.Client
	list := &corev1.SecretList{}
	opt := client.InNamespace(ns)

	if err := c.List(context.Background(),
		opt, list); err != nil {
		return "", err
	}
	for _, item := range list.Items {
		if item.Annotations == nil || item.Data == nil {
			continue
		}

		sa, ok := item.Annotations["kubernetes.io/service-account.name"]
		if !ok || sa != serviceAccountName {
			continue
		}

		token, ok := item.Data["token"]
		if !ok {
			continue
		}

		if len(token) > 0 {
			return string(token), nil
		}
	}
	return "", fmt.Errorf("valid token could not be found")
}

func removeSubject(x rbacv1.Subject, clusterclient serde.Provider, name string) error {
	t := fmt.Sprintf("Removing subject %s", x.Name)
	log.Info(t)
	tempsubjects := make([]rbacv1.Subject, 0)
	temp := rbacv1.Subject{
		Kind:      "ServiceAccount",
		Name:      x.Name,
		Namespace: "default",
	}
	tempsubjects = append(tempsubjects, temp)
	crbinding := &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: rbacv1.SchemeGroupVersion.String(),
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-admins-clusterrolebinding",
			Namespace: "default",
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
			APIGroup: "rbac.authorization.k8s.io",
		},
		Subjects: tempsubjects,
	}
	clusterapi.DeleteClusterRoleBinding(clusterclient, crbinding)
	clusterapi.DeleteServiceAccount(clusterclient, x.Name, "default")
	return nil
}

func (r *ReconcileTenant) deleteExternalResources(instance *tenancyv1alpha1.Tenant) error {

	//
	// delete any external resources associated with the tenant
	//
	// Ensure that delete implementation is idempotent and safe to invoke
	// multiple types for same object.

	temp := fmt.Sprintf("Deleting tenant %s", instance.Name)
	log.Info(temp)
	//Call Cluster Delete here
	clusterclient := clusterapi.GetCluster()

	for _, x := range instance.Spec.TenantAdmins {
		err := removeSubject(x, clusterclient, instance.Name)
		if err != nil {
			return err
		}
	}
	/*
		all := []string{"*"}
		none := []string{""}
		rules := rbacv1.PolicyRule{
			Verbs:           all,
			APIGroups:       all,
			Resources:       all,
			NonResourceURLs: none,
		}
		temprules := make([]rbacv1.PolicyRule, 0)
		temprules = append(temprules, rules)
		cr := &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "tenant-admins-clusterrole",
				Namespace: "default",
			},
			Rules: temprules,
		}
		err := clusterapi.DeleteClusterRole(clusterclient, cr)
		if err != nil {
			temp := fmt.Sprintf("Unable to delete cluster roles  %s for  tenant %s", x.Name, instance.Name)
			log.Info(temp)
			return err
		}
	*/

	clusterapi.DeleteCluster()
	//Delete the tenant namespace
	expectedOwnerRef := metav1.OwnerReference{
		APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(),
		Kind:       "Tenant",
		Name:       instance.Name,
		UID:        instance.UID,
	}

	tenantAdminNs := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Namespace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            instance.Spec.TenantAdminNamespaceName,
			OwnerReferences: []metav1.OwnerReference{expectedOwnerRef},
		},
	}
	r.Client.Delete(context.TODO(), tenantAdminNs)
	r.DeleteTenancy(instance.Name, instance.Spec.TenantAdminNamespaceName)
	tenancyMutex.Lock()
	delete(tenancymap, instance.Name)
	tenancyMutex.Unlock()

	/*
		for _, x := range instance.Spec.TenantAdmins {
			r.TenantDeleteKey(instance.Name, x.Name)
		}*/
	return nil
}

// Helper functions to check and remove string from a slice of strings.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}

func getTenantAdminNames(admins []rbacv1.Subject) []string {
	temp := make([]string, 0)
	for _, x := range admins {
		temp = append(temp, x.Name)
	}
	return temp
}

//To talk to vault generate a per tenancy secret token with Service account. Use this
// as interface with vault
func (r *ReconcileTenant) GenerateVaultToken() (string, error) {
	token, err := r.GetAuthorizationTokenfromSecret("default", "tenant-controller-secret")
	if err != nil {
		log.Info("Not able to token from tenant secret for vault ")
		return "", err
	}
	fmt.Printf("\n----------------\nvault token is :%v\n", token)
	return token, nil
}

func (r *ReconcileTenant) ConnecttoVault(ns string, tenancyname string) (secret.Store, error) {

	InitVaultRootToken, err := r.GenerateVaultToken()
	if err != nil {
		log.Info("Not able to generate token for tenancy vault secret")
		return nil, err
	}

	vault, err := secret.NewVaultStore("spektra/"+tenancyname, VaultAddress, InitVaultRootToken)
	if err != nil {
		log.Info("Not able to create Vault Store ")
		return nil, err
	}
	return vault, nil
}

func (r *ReconcileTenant) GetTenancy(ns string, tenancyname string) (secret.Store, error) {
	vault, err := r.ConnecttoVault(ns, tenancyname)
	if err != nil {
		return nil, err
	}
	return vault, nil
}

func (r *ReconcileTenant) DeleteTenancy(tenancyname string, ns string) error {
	_ = secret.TenancyCreateRequest{Name: tenancyname,
		Username: ns,
		Password: "",
	}
	return nil
}

func (r *ReconcileTenant) UpdateTargetToken(token string, ns string, tenancyname string, username string) error {
	vault, err := r.GetTenancy(ns, tenancyname)
	if err != nil {
		return err
	}

	// The parameter to used here is clustername + username. This is the key to update target tokens. Remember
	// we don't need tenancy since the Kv is per tenancy
	key := "azure" + username
	if err := vault.KvDataSet(VaultKvPath, key, &secret.KvEntry{
		Data: map[string][]byte{
			"targettoken": []byte(token),
		},
	}, nil); err != nil {
		fmt.Printf("Update Target Token:\n%v\n", err)
		return err
	}
	return nil
}

func (r *ReconcileTenant) UpdateMasterToken(token string, ns string, tenancyname string, username string) error {
	vault, err := r.GetTenancy(ns, tenancyname)
	if err != nil {
		return err
	}
	key := tenancyname + "-" + username
	if err := vault.KvDataSet(VaultKvPath, key, &secret.KvEntry{
		Data: map[string][]byte{
			"mastertoken": []byte(token),
		},
	}, nil); err != nil {
		fmt.Printf("Update Master Token:\n%v\n", err)
		return err
	}
	return nil
}

func (r *ReconcileTenant) CreateTenancy(ns string, tenancyname string) error {
	vault, err := r.ConnecttoVault(ns, tenancyname)
	if err != nil {
		return err
	}
	t := secret.TenancyCreateRequest{Name: tenancyname,
		Username: ns,
		Password: "",
	}

	out, errt := vault.TenancyCreate(&t)
	if errt != nil {
		fmt.Printf("valut error : %v\n", errt)
		return errt
	}

	jb, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(jb))

	return nil
}

// difference returns the elements in `a` that aren't in `b`.
func difference(a, b []rbacv1.Subject) []rbacv1.Subject {
	mb := make(map[string]*rbacv1.Subject, len(b))
	for _, x := range b {
		mb[x.Name] = &rbacv1.Subject{}
	}
	var diff []rbacv1.Subject
	for _, x := range a {
		if _, found := mb[x.Name]; !found {
			diff = append(diff, x)
		}
	}
	return diff
}

func finddeletedadmins(instance *tenancyv1alpha1.Tenant) []rbacv1.Subject {

	tenancyMutex.RLock()
	//Find elements in previous update that are missing now, so we can delete those users
	x := difference(tenancymap[instance.Name], instance.Spec.TenantAdmins)
	tenancyMutex.RUnlock()
	return x
}

func (r *ReconcileTenant) removetenantadmins(admins []rbacv1.Subject, name string,
	expectedOwnerRef metav1.OwnerReference, ns string) error {

	clusterclient := clusterapi.GetCluster()
	for _, x := range admins {
		//CRB Delete
		// SA delete
		err := removeSubject(x, clusterclient, name)
		if err != nil {
			return err
		}
		temp := fmt.Sprintf("Removing admin : %s from tenant : %s", x.Name, name)
		log.Info(temp)
	}

	//Add code to remove rolebinding and ClusterBinding
	//  Roles and ClutserRoles can be removed when the tenancy is deleted
	crbindingName := fmt.Sprintf("%s-tenant-admins-rolebinding", name)
	crName := fmt.Sprintf("%s-tenant-admin-role", name)
	crbinding := &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: rbacv1.SchemeGroupVersion.String(),
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            crbindingName,
			OwnerReferences: []metav1.OwnerReference{expectedOwnerRef},
		},
		Subjects: admins,
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     crName,
		},
	}
	r.Client.Delete(context.TODO(), crbinding)

	rbinding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: rbacv1.SchemeGroupVersion.String(),
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            "tenant-admins-rolebinding",
			Namespace:       ns,
			OwnerReferences: []metav1.OwnerReference{expectedOwnerRef},
		},
		Subjects: admins,
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     "tenant-admin-role",
		},
	}

	r.Client.Delete(context.TODO(), rbinding)

	return nil
}

// Reconcile reads that state of the cluster for a Tenant object and makes changes based on the state read
// and what is in the Tenant.Spec
// Automatically generate RBAC rules to allow the Controller to read and write related resources
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;create;update;patch
func (r *ReconcileTenant) Reconcile(request reconcile.Request) (reconcile.Result, error) {

	doOnce.Do(func() {
		log.Info("Initialize mutex and make tenancy map")
		tenancyMutex = &sync.RWMutex{}
		tenancymap = make(map[string][]rbacv1.Subject, 0)
		// Other things to do once
	})
	// Fetch the Tenant instance
	instance := &tenancyv1alpha1.Tenant{}
	// Tenant is a cluster scoped CR, we should clear the namespace field in request
	request.NamespacedName.Namespace = ""
	err := r.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	expectedOwnerRef := metav1.OwnerReference{
		APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(),
		Kind:       "Tenant",
		Name:       instance.Name,
		UID:        instance.UID,
	}

	// name of our custom finalizer
	tenantFinalizerName := "tenancy.finalizers.diamanti.com"

	// examine DeletionTimestamp to determine if object is under deletion
	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object. This is equivalent
		// registering our finalizer.
		if !containsString(instance.ObjectMeta.Finalizers, tenantFinalizerName) {
			instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers, tenantFinalizerName)
			if err := r.Update(context.Background(), instance); err != nil {
				return reconcile.Result{}, err
			}
		}
	} else {
		// The object is being deleted
		if containsString(instance.ObjectMeta.Finalizers, tenantFinalizerName) {
			// our finalizer is present, so lets handle any external dependency
			if err := r.deleteExternalResources(instance); err != nil {
				// if fail to delete the external dependency here, return with error
				// so that it can be retried
				return reconcile.Result{}, err
			}

			// remove our finalizer from the list and update it.
			instance.ObjectMeta.Finalizers = removeString(instance.ObjectMeta.Finalizers, tenantFinalizerName)
			if err := r.Update(context.Background(), instance); err != nil {
				return reconcile.Result{}, err
			}
		}

		return reconcile.Result{}, err
	}

	tenancyMutex.RLock()
	_, ok := tenancymap[instance.Name]
	tenancyMutex.RUnlock()
	if ok == true {
		deletedadmins := finddeletedadmins(instance)
		r.removetenantadmins(deletedadmins, instance.Name, expectedOwnerRef, instance.Spec.TenantAdminNamespaceName)
	}

	log.Info("Successfully added finalizer for tenant object ")
	// Create tenantAdminNamespace
	if instance.Spec.TenantAdminNamespaceName != "" {
		nsList := &corev1.NamespaceList{}
		err := r.List(context.TODO(), &client.ListOptions{}, nsList)
		if err != nil {
			return reconcile.Result{}, err
		}
		foundNs := false
		for _, each := range nsList.Items {
			if each.Name == instance.Spec.TenantAdminNamespaceName {
				foundNs = true
				// Check OwnerReference
				isOwner := false
				for _, ownerRef := range each.OwnerReferences {
					if ownerRef == expectedOwnerRef {
						isOwner = true
						break
					}
				}
				if !isOwner {
					err = fmt.Errorf("TenantAdminNamespace %v is owned by %v", each.Name, each.OwnerReferences)
					return reconcile.Result{}, err
				}
				break
			}
		}
		if !foundNs {
			tenantAdminNs := &corev1.Namespace{
				TypeMeta: metav1.TypeMeta{
					APIVersion: corev1.SchemeGroupVersion.String(),
					Kind:       "Namespace",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:            instance.Spec.TenantAdminNamespaceName,
					OwnerReferences: []metav1.OwnerReference{expectedOwnerRef},
				},
			}
			if err := r.Client.Create(context.TODO(), tenantAdminNs); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	for _, x := range instance.Spec.TenantAdmins {
		obj := &corev1.ServiceAccount{}
		obj.Name = x.Name
		obj.Kind = "ServiceAccount"
		obj.APIVersion = "v1"
		obj.Namespace = instance.Spec.TenantAdminNamespaceName

		if err := r.clientApply(obj); err != nil {
			if !errors.IsAlreadyExists(err) {
				return reconcile.Result{}, err
			}
		}
	}

	// Create RBACs for tenantAdmins, allow them to access tenant CR and tenantAdminNamespace.
	if instance.Spec.TenantAdmins != nil {
		// First, create cluster roles to allow them to access tenant CR and tenantAdminNamespace.
		crName := fmt.Sprintf("%s-tenant-admin-role", instance.Name)
		cr := &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:            crName,
				OwnerReferences: []metav1.OwnerReference{expectedOwnerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:         []string{"get", "list", "watch", "update", "patch", "delete"},
					APIGroups:     []string{tenancyv1alpha1.SchemeGroupVersion.Group},
					Resources:     []string{"tenants"},
					ResourceNames: []string{instance.Name},
				},
				{
					Verbs:         []string{"get", "list", "watch"},
					APIGroups:     []string{""},
					Resources:     []string{"namespaces"},
					ResourceNames: []string{instance.Spec.TenantAdminNamespaceName},
				},
			},
		}
		if err := r.clientApply(cr); err != nil {
			if !errors.IsAlreadyExists(err) {
				return reconcile.Result{}, err
			}
		}
		crbindingName := fmt.Sprintf("%s-tenant-admins-rolebinding", instance.Name)
		crbinding := &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:            crbindingName,
				OwnerReferences: []metav1.OwnerReference{expectedOwnerRef},
			},
			Subjects: instance.Spec.TenantAdmins,
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     crName,
			},
		}
		if err := r.clientApply(crbinding); err != nil {
			if !errors.IsAlreadyExists(err) {
				return reconcile.Result{}, err
			}
		}
		tenantnamespacepoilcy := rbacv1.PolicyRule{
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			APIGroups: []string{tenancyv1alpha1.SchemeGroupVersion.Group},
			Resources: []string{"tenantnamespaces"},
		}
		// Pods being added to TA Ns to debug RBAC rules
		podspolicy := rbacv1.PolicyRule{
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			APIGroups: []string{""},
			Resources: []string{"pods"},
		}
		// Second, create namespace role to allow them to create tenantnamespace CR in tenantAdminNamespace.
		role := &rbacv1.Role{
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "Role",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:            "tenant-admin-role",
				Namespace:       instance.Spec.TenantAdminNamespaceName,
				OwnerReferences: []metav1.OwnerReference{expectedOwnerRef},
			},
			Rules: []rbacv1.PolicyRule{tenantnamespacepoilcy, podspolicy},
		}
		if err := r.clientApply(role); err != nil {
			if !errors.IsAlreadyExists(err) {
				return reconcile.Result{}, err
			}
		}
		rbinding := &rbacv1.RoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "RoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:            "tenant-admins-rolebinding",
				Namespace:       instance.Spec.TenantAdminNamespaceName,
				OwnerReferences: []metav1.OwnerReference{expectedOwnerRef},
			},
			Subjects: instance.Spec.TenantAdmins,
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "Role",
				Name:     "tenant-admin-role",
			},
		}
		if err := r.clientApply(rbinding); err != nil {
			if !errors.IsAlreadyExists(err) {
				return reconcile.Result{}, err
			}
		}
	}

	log.Info("Successfully added RBAC and SA on master cluster")
	err = r.CreateTenancy(instance.Spec.TenantAdminNamespaceName, instance.Name)
	if err != nil {
		log.Info("Unable to Create tenancy in Vault")
		return reconcile.Result{}, err
	}
	clusterclient := clusterapi.CreateCluster()
	// Handle target cluster related tasks
	for _, x := range instance.Spec.TenantAdmins {
		// There should be a loop here for all clusters in the tenancy
		// XXX: TODO make it a part of the clutser API integration
		clusterapi.CreateServiceAccount(clusterclient, x.Name, "default")
		if err != nil {
			return reconcile.Result{}, err
		}
		clusterapi.CreateSecret(clusterclient, x.Name, "default")
		if err != nil {
			return reconcile.Result{}, err
		}

		token, errtok := clusterapi.GetAuthorizationToken(clusterclient, "default", x.Name)
		if errtok != nil {
			return reconcile.Result{}, err
		}

		all := []string{"*"}
		none := []string{""}
		rules := rbacv1.PolicyRule{
			Verbs:           all,
			APIGroups:       all,
			Resources:       all,
			NonResourceURLs: none,
		}
		temprules := make([]rbacv1.PolicyRule, 0)
		temprules = append(temprules, rules)
		cr := &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "tenant-admins-clusterrole",
				Namespace: "default",
			},
			Rules: temprules,
		}
		err = clusterapi.CreateClusterRole(clusterclient, cr)
		if err != nil {
			return reconcile.Result{}, err
		}

		tempsubjects := make([]rbacv1.Subject, 0)
		tempsub := rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      x.Name,
			Namespace: "default",
		}
		tempsubjects = append(tempsubjects, tempsub)
		crbinding := &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "tenant-admins-clusterrolebinding",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     "cluster-admin",
				APIGroup: "rbac.authorization.k8s.io",
			},
			Subjects: tempsubjects,
		}
		err = clusterapi.CreateClusterRoleBinding(clusterclient, crbinding)
		if err != nil {
			return reconcile.Result{}, err
		}
		err = r.UpdateTargetToken(token, instance.Spec.TenantAdminNamespaceName, instance.Name, x.Name)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	log.Info("Created Tenancy in target clusters")

	for _, x := range instance.Spec.TenantAdmins {
		token, errtok := r.GetAuthorizationToken(instance.Spec.TenantAdminNamespaceName, x.Name)
		if errtok != nil {
			return reconcile.Result{}, err
		}

		_, err := clusterapi.CreateKubeconfig(x.Name, instance.Spec.TenantAdminNamespaceName)
		if err != nil {
			return reconcile.Result{}, err
		}

		err = r.UpdateMasterToken(token, instance.Spec.TenantAdminNamespaceName, instance.Name, x.Name)
		if err != nil {
			return reconcile.Result{}, err
		}
	}
	tenancyMutex.Lock()
	tenancymap[instance.Name] = instance.Spec.TenantAdmins
	tenancyMutex.Unlock()
	log.Info("Created Tenancy in CR and Vault")

	return reconcile.Result{}, nil
}

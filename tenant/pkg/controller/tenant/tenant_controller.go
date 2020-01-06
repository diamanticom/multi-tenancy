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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/diamanticom/multi-tenancy/tenant/clusterapi"
	tenancyv1alpha1 "github.com/diamanticom/multi-tenancy/tenant/pkg/apis/tenancy/v1alpha1"
	"github.com/diamanticom/multi-tenancy/tenant/tenantdb"
	"io/ioutil"
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
)

var log = logf.Log.WithName("controller")

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

func (r *ReconcileTenant) deleteExternalResources(instance *tenancyv1alpha1.Tenant) error {
	//
	// delete any external resources associated with the tenant
	//
	// Ensure that delete implementation is idempotent and safe to invoke
	// multiple types for same object.
	fmt.Printf("XXX-----Deleting Tenancy:%s-----XXX", instance.Name)
	//Call Cluster Delete here
	clusterapi.DeleteCluster()

	key := tenantdb.CreateTenantKey(instance.Name)
	return tenantdb.TenantDeleteKey(key)
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

// Reconcile reads that state of the cluster for a Tenant object and makes changes based on the state read
// and what is in the Tenant.Spec
// Automatically generate RBAC rules to allow the Controller to read and write related resources
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;create;update;patch
func (r *ReconcileTenant) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	var targetkubeconfig, masterkubeconfig []string
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
	// You cannot change tenancy names in Updates. No way to handle it. It will be treated as a create
	if err := tenantdb.TenantGetKey(instance.Name); err != nil {
		// Handle Updates Here, only allow tenantAdmin Add/Delete/renames

		fmt.Println("XXXX------In the Reconcile-----XXXX4")
	} else {
		//Remote Cluster stuff
		{
			clusterclient, kubeconfig := clusterapi.CreateCluster()
			targetkubeconfig = append(targetkubeconfig, kubeconfig)

			for _, x := range instance.Spec.TenantAdmins {
				if err := clusterapi.CreateServiceAccount(clusterclient, x.Name, "kube-system"); err != nil {
					return reconcile.Result{}, err
				}

				all := []string{"*"}
				rules := rbacv1.PolicyRule{
					Verbs:           all,
					APIGroups:       all,
					Resources:       all,
					NonResourceURLs: all,
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
						Namespace: "kube-system",
					},
					Rules: temprules,
				}
				if err := clusterapi.CreateClusterRole(clusterclient, cr); err != nil {
					return reconcile.Result{}, err
				}
				tempsubjects := make([]rbacv1.Subject, 0)
				temp := rbacv1.Subject{
					Kind:      "ServiceAccount",
					Name:      x.Name,
					Namespace: "kube-system",
				}
				tempsubjects = append(tempsubjects, temp)
				crbinding := &rbacv1.ClusterRoleBinding{
					TypeMeta: metav1.TypeMeta{
						APIVersion: rbacv1.SchemeGroupVersion.String(),
						Kind:       "ClusterRoleBinding",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tenant-admins-clusterrolebinding",
						Namespace: "kube-system",
					},
					RoleRef: rbacv1.RoleRef{
						Kind:     "ClusterRole",
						Name:     "cluster-admin",
						APIGroup: "rbac.authorization.k8s.io",
					},
					Subjects: tempsubjects,
				}
				if err := clusterapi.CreateClusterRoleBinding(clusterclient, crbinding); err != nil {
					return reconcile.Result{}, err
				}
			}
		}

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
				return reconcile.Result{}, err
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
				return reconcile.Result{}, err
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
				Rules: []rbacv1.PolicyRule{
					{
						Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
						APIGroups: []string{tenancyv1alpha1.SchemeGroupVersion.Group},
						Resources: []string{"tenantnamespaces"},
					},
				},
			}
			if err := r.clientApply(role); err != nil {
				return reconcile.Result{}, err
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
				return reconcile.Result{}, err
			}
		}
		for _, x := range instance.Spec.TenantAdmins {
			mk, err := CreateSA(x.Name, instance.Spec.TenantAdminNamespaceName)
			if err != nil {
				return reconcile.Result{}, err
			}
			masterkubeconfig = append(masterkubeconfig, mk)
		}
		temp := make([]string, 0)
		tdb := tenantdb.TenantData{MasterKubeConfig: masterkubeconfig,
			TargetKubeConfig: targetkubeconfig,
			TargetSA:         temp,
			MasterSA:         temp,
		}
		key := tenantdb.CreateTenantKey(instance.Name)
		if err := tenantdb.TenantStoreKey(key, &tdb); err != nil {
			panic(err)
		}
	}
	return reconcile.Result{}, nil

}

func CreateSA(SA string, NS string) (string, error) {
	var out bytes.Buffer
	dir, err := ioutil.TempDir("/tmp", "kubeconfig")
	if err != nil {
		panic(err)
	}
	defer os.Remove(dir)
	fmt.Println(dir)

	file, err := ioutil.TempFile(dir, "kubeconfig")
	if err != nil {
		panic(err)
	}
	fmt.Println(file.Name())

	cmd := exec.Command("./kubernetes_add_service_account_kubeconfig.sh", SA, NS, file.Name())
	cmd.Stdout = &out
	execErr := cmd.Run()
	return out.String(), execErr

}

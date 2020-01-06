package clusterapi

import (
	"gitlab.eng.diamanti.com/software/mcm.git/dmc/pkg/serde"
	//"k8s.io/client-go/rest"
	"context"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func DeleteCluster() {

}

// Write Cluster API code here, return kubeconfig
func CreateCluster() (serde.Provider, string) {

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", "/Users/kgunjikar/code/src/capi-quickstart-azure.kubeconfig")
	if err != nil {
		panic(err.Error())
	}

	/*
		config, err := rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		} */

	// create the clientset
	c, err := serde.NewProvider(config)
	if err != nil {
		panic(err.Error())
	}
	// Mock the kubeconfig for now
	return c, "Testing"
}

func CreateServiceAccount(k serde.Provider, name string, ns string) error {
	client, err := k.GetClient()
	if err != nil {
		panic(err)
	}
	obj := &corev1.ServiceAccount{}
	obj.Name = name
	obj.Kind = "ServiceAccount"
	obj.APIVersion = "v1"
	obj.Namespace = ns

	return client.Create(context.Background(), obj)
}

func DeleteServiceAccount(k serde.Provider, name string, ns string) error {
	client, err := k.GetClient()
	if err != nil {
		panic(err)
	}

	obj := &corev1.ServiceAccount{}
	obj.Name = name
	obj.Kind = "ServiceAccount"
	obj.APIVersion = "v1"
	obj.Namespace = ns

	return client.Delete(context.Background(), obj)
}

func DeleteClusterRoleBinding(k serde.Provider, crb *rbacv1.ClusterRoleBinding) error {
	client, err := k.GetClient()
	if err != nil {
		panic(err)
	}
	return client.Delete(context.Background(), crb)
}

func CreateClusterRoleBinding(k serde.Provider, crb *rbacv1.ClusterRoleBinding) error {
	client, err := k.GetClient()
	if err != nil {
		panic(err)
	}
	return client.Create(context.Background(), crb)
}

func DeleteClusterRole(k serde.Provider, cr *rbacv1.ClusterRole) error {
	client, err := k.GetClient()
	if err != nil {
		panic(err)
	}
	return client.Delete(context.Background(), cr)
}

func CreateClusterRole(k serde.Provider, cr *rbacv1.ClusterRole) error {
	client, err := k.GetClient()
	if err != nil {
		panic(err)
	}
	return client.Create(context.Background(), cr)
}

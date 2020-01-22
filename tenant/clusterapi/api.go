package clusterapi

import (
	"gitlab.eng.diamanti.com/software/mcm.git/dmc/pkg/serde"
	//"k8s.io/client-go/rest"
	"context"
	"fmt"
	"io/ioutil"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var targetclient serde.Provider

func CreateKubeconfig(sa string, ns string) ([]string, error) {
	// Create temp folder for the test case (without a CA)
	tmpdir, err := ioutil.TempDir("", "")
	defer os.RemoveAll(tmpdir)

	dir, err := ioutil.TempDir("/tmp", "kubeconfig")
	if err != nil {
		return []string{""}, err
	}
	defer os.Remove(dir)
	fmt.Println(dir)

	file, err := ioutil.TempFile(dir, "kubeconfig")
	if err != nil {
		return []string{""}, err
	}
	fmt.Println(file.Name())

	/*
		// Creates an InitConfiguration pointing to the pkidir folder
		cfg := &kubeadmapi.InitConfiguration{
			ClusterConfiguration: kubeadmapi.ClusterConfiguration{
				CertificatesDir: tmpdir,
			},
		}

		if err := CreateKubeConfigFile(file.Name(), dir); err != nil {
			return "", err
		}
	*/
	dat, err := ioutil.ReadFile(file.Name())
	if err != nil {
		return []string{""}, err
	}
	temp := make([]string, 0)
	temp = append(temp, (string(dat) + "Testing"))

	return temp, nil
}

func DeleteCluster() {

}

func GetCluster() serde.Provider {
	return targetclient
}

// Write Cluster API code here, return kubeconfig
func CreateCluster() serde.Provider {

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", "./capi-quickstart-azure.kubeconfig")
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
	targetclient = c
	// Mock the kubeconfig for now
	return c
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

	if err := client.Create(context.Background(), obj); err != nil {
		if errors.IsAlreadyExists(err) {
			err = client.Update(context.Background(), obj)
		}
	}
	return err
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
	if err := client.Create(context.Background(), crb); err != nil {
		if errors.IsAlreadyExists(err) {
			err = client.Update(context.Background(), crb)
		}
	}
	return err
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
	if err := client.Create(context.Background(), cr); err != nil {
		if errors.IsAlreadyExists(err) {
			err = client.Update(context.Background(), cr)
		}
	}
	return err
}

func DeleteSecret(k serde.Provider, name string, ns string) error {
	client, err := k.GetClient()
	if err != nil {
		panic(err)
	}
	objs := &corev1.Secret{}
	objs.Name = name
	objs.Kind = "Secret"
	objs.Namespace = ns
	objs.APIVersion = "v1"
	objs.Type = corev1.SecretTypeServiceAccountToken

	return client.Delete(context.Background(), objs)
}

func CreateSecret(k serde.Provider, name string, ns string) error {
	client, err := k.GetClient()
	if err != nil {
		panic(err)
	}

	objs := &corev1.Secret{}
	objs.Name = name
	objs.Namespace = ns
	objs.Kind = "Secret"
	objs.APIVersion = "v1"
	objs.Type = corev1.SecretTypeServiceAccountToken

	if err = client.Create(context.Background(), objs); err != nil {
		if errors.IsAlreadyExists(err) {
			err = client.Update(context.Background(), objs)
		}
	}
	return err
}

func GetAuthorizationToken(k serde.Provider, ns string, serviceAccountName string) (string, error) {
	c, err := k.GetClient()
	if err != nil {
		panic(err)
	}
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

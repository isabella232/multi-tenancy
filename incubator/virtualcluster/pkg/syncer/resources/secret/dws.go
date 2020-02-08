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

package secret

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"

	"github.com/kubernetes-sigs/multi-tenancy/incubator/virtualcluster/pkg/syncer/constants"
	"github.com/kubernetes-sigs/multi-tenancy/incubator/virtualcluster/pkg/syncer/conversion"
	"github.com/kubernetes-sigs/multi-tenancy/incubator/virtualcluster/pkg/syncer/reconciler"
	"github.com/kubernetes-sigs/multi-tenancy/incubator/virtualcluster/pkg/syncer/utils"
)

func (c *controller) StartDWS(stopCh <-chan struct{}) error {
	if !cache.WaitForCacheSync(stopCh, c.secretSynced) {
		return fmt.Errorf("failed to wait for caches to sync")
	}
	return c.multiClusterSecretController.Start(stopCh)
}

// The reconcile logic for tenant master secret informer
func (c *controller) Reconcile(request reconciler.Request) (reconciler.Result, error) {
	klog.Infof("reconcile secret %s/%s %s event for cluster %s", request.Namespace, request.Name, request.Event, request.Cluster.Name)

	switch request.Event {
	case reconciler.AddEvent:
		err := c.reconcileSecretCreate(request.Cluster.Name, request.Namespace, request.Name, request.Obj.(*v1.Secret))
		if err != nil {
			klog.Errorf("failed reconcile secret %s/%s CREATE of cluster %s %v", request.Namespace, request.Name, request.Cluster.Name, err)
			return reconciler.Result{Requeue: true}, err
		}
	case reconciler.UpdateEvent:
		err := c.reconcileSecretUpdate(request.Cluster.Name, request.Namespace, request.Name, request.Obj.(*v1.Secret))
		if err != nil {
			klog.Errorf("failed reconcile secret %s/%s CREATE of cluster %s %v", request.Namespace, request.Name, request.Cluster.Name, err)
			return reconciler.Result{Requeue: true}, err
		}
	case reconciler.DeleteEvent:
		err := c.reconcileSecretRemove(request.Cluster.Name, request.Namespace, request.Name, request.Obj.(*v1.Secret))
		if err != nil {
			klog.Errorf("failed reconcile secret %s/%s DELETE of cluster %s %v", request.Namespace, request.Name, request.Cluster.Name, err)
			return reconciler.Result{Requeue: true}, err
		}
	}
	return reconciler.Result{}, nil
}

func (c *controller) reconcileSecretCreate(cluster, namespace, name string, secret *v1.Secret) error {
	switch secret.Type {
	case v1.SecretTypeServiceAccountToken:
		return c.reconcileServiceAccountSecretUpdate(cluster, namespace, name, secret)
	default:
		return c.reconcileNormalSecretCreate(cluster, namespace, name, secret)
	}
}

func (c *controller) reconcileServiceAccountSecretUpdate(cluster, namespace, name string, secret *v1.Secret) error {
	targetNamespace := conversion.ToSuperMasterNamespace(cluster, namespace)

	saName := secret.Annotations[v1.ServiceAccountNameKey]

	pSecret, err := utils.GetSecret(c.secretClient, targetNamespace, saName)
	if err != nil {
		return err
	}

	needUpdate := false

	if pSecret.Labels[constants.SyncStatusKey] != constants.SyncStatusReady {
		needUpdate = true
		if len(pSecret.Labels) == 0 {
			pSecret.Labels = make(map[string]string)
		}
		pSecret.Labels[constants.SyncStatusKey] = constants.SyncStatusReady
	}

	if !equality.Semantic.DeepEqual(secret.Data, pSecret.Data) {
		needUpdate = true
		pSecret.Data = secret.Data
	}

	if !needUpdate {
		return nil
	}

	_, err = c.secretClient.Secrets(targetNamespace).Update(pSecret)
	if err != nil {
		return err
	}
	return nil
}

func (c *controller) reconcileNormalSecretCreate(cluster, namespace, name string, secret *v1.Secret) error {
	targetNamespace := conversion.ToSuperMasterNamespace(cluster, namespace)
	_, err := c.secretLister.Secrets(targetNamespace).Get(name)
	if err == nil {
		return c.reconcileNormalSecretUpdate(cluster, namespace, name, secret)
	}

	newObj, err := conversion.BuildMetadata(cluster, targetNamespace, secret)
	if err != nil {
		return err
	}

	_, err = c.secretClient.Secrets(targetNamespace).Create(newObj.(*v1.Secret))
	if errors.IsAlreadyExists(err) {
		klog.Infof("secret %s/%s of cluster %s already exist in super master", namespace, name, cluster)
		return nil
	}

	return err
}

func (c *controller) reconcileSecretUpdate(cluster, namespace, name string, secret *v1.Secret) error {
	switch secret.Type {
	case v1.SecretTypeServiceAccountToken:
		return c.reconcileServiceAccountSecretUpdate(cluster, namespace, name, secret)
	default:
		return c.reconcileNormalSecretUpdate(cluster, namespace, name, secret)
	}
}

func (c *controller) reconcileNormalSecretUpdate(cluster, namespace, name string, vSecret *v1.Secret) error {
	targetNamespace := conversion.ToSuperMasterNamespace(cluster, namespace)
	pSecret, err := c.secretLister.Secrets(targetNamespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	spec, err := c.multiClusterSecretController.GetSpec(cluster)
	if err != nil {
		return err
	}
	updatedSecret := conversion.Equality(spec).CheckSecretEquality(pSecret, vSecret)
	if updatedSecret != nil {
		pSecret, err = c.secretClient.Secrets(targetNamespace).Update(updatedSecret)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *controller) reconcileSecretRemove(cluster, namespace, name string, secret *v1.Secret) error {
	targetNamespace := conversion.ToSuperMasterNamespace(cluster, namespace)
	opts := &metav1.DeleteOptions{
		PropagationPolicy: &constants.DefaultDeletionPolicy,
	}
	err := c.secretClient.Secrets(targetNamespace).Delete(name, opts)
	if errors.IsNotFound(err) {
		klog.Warningf("secret %s/%s of cluster is not found in super master", namespace, name)
		return nil
	}
	return err
}

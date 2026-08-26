//nolint:goconst // Repeated literals keep each resolver fixture self-contained.
package safety

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDeploymentPodResolverSelectsPostActionReplacement(t *testing.T) {
	actionStartedAt := time.Unix(100, 0)
	objects := deploymentPodObjects(actionStartedAt)
	resolver := &DeploymentPodResolver{Reader: fakeClientWithObjects(t, objects...)}

	pod, err := resolver.Resolve(context.Background(), VerificationTarget{
		OriginalPod:     types.NamespacedName{Name: "checkout-old", Namespace: "shop"},
		Deployment:      types.NamespacedName{Name: "checkout", Namespace: "shop"},
		ContainerName:   "app",
		ActionStartedAt: actionStartedAt,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if pod.Name != "checkout-new" {
		t.Fatalf("resolved Pod = %q, want checkout-new", pod.Name)
	}
}

func TestDeploymentPodResolverDoesNotUseOriginalPod(t *testing.T) {
	actionStartedAt := time.Unix(100, 0)
	objects := deploymentPodObjects(actionStartedAt)
	deployment, replicaSet := objects[0], objects[1]
	original := deploymentPod(
		"checkout-old",
		"original-uid",
		replicaSet.(*appsv1.ReplicaSet),
		actionStartedAt.Add(-time.Minute),
	)
	resolver := &DeploymentPodResolver{Reader: fakeClientWithObjects(t, deployment, replicaSet, original)}

	_, err := resolver.Resolve(context.Background(), VerificationTarget{
		OriginalPod:     types.NamespacedName{Name: "checkout-old", Namespace: "shop"},
		Deployment:      types.NamespacedName{Name: "checkout", Namespace: "shop"},
		ContainerName:   "app",
		ActionStartedAt: actionStartedAt,
	})
	if !errors.Is(err, ErrDeploymentPodNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrDeploymentPodNotFound", err)
	}
}

func TestDeploymentPodResolverRejectsPreActionAndForeignPods(t *testing.T) {
	actionStartedAt := time.Unix(100, 0)
	objects := deploymentPodObjects(actionStartedAt)
	deployment, replicaSet, preAction, foreign := objects[0], objects[1], objects[2], objects[3]
	preAction.(*corev1.Pod).CreationTimestamp = metav1.NewTime(actionStartedAt.Add(-time.Second))
	resolver := &DeploymentPodResolver{
		Reader: fakeClientWithObjects(t, deployment, replicaSet, preAction, foreign),
	}

	_, err := resolver.Resolve(context.Background(), VerificationTarget{
		OriginalPod:     types.NamespacedName{Name: "checkout-old", Namespace: "shop"},
		Deployment:      types.NamespacedName{Name: "checkout", Namespace: "shop"},
		ContainerName:   "app",
		ActionStartedAt: actionStartedAt,
	})
	if !errors.Is(err, ErrDeploymentPodNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrDeploymentPodNotFound", err)
	}
}

func deploymentPodObjects(actionStartedAt time.Time) []runtime.Object {
	controller := true
	deploymentUID := types.UID("deployment-uid")
	replicaSetUID := types.UID("replicaset-uid")
	foreignReplicaSetUID := types.UID("foreign-replicaset-uid")

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", UID: deploymentUID},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
		},
	}
	replicaSet := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout-rs",
			Namespace: "shop",
			UID:       replicaSetUID,
			Labels:    map[string]string{"app": "checkout"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "checkout",
				UID:        deploymentUID,
				Controller: &controller,
			}},
		},
	}
	replacement := deploymentPod(
		"checkout-new",
		"replacement-uid",
		replicaSet,
		actionStartedAt.Add(time.Second),
	)
	foreign := deploymentPod(
		"foreign-pod",
		"foreign-pod-uid",
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "foreign-rs", Namespace: "shop", UID: foreignReplicaSetUID,
		}},
		actionStartedAt.Add(2*time.Second),
	)
	return []runtime.Object{deployment, replicaSet, replacement, foreign}
}

func deploymentPod(
	name string,
	uid types.UID,
	replicaSet *appsv1.ReplicaSet,
	createdAt time.Time,
) *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "shop",
			UID:               uid,
			CreationTimestamp: metav1.NewTime(createdAt),
			Labels:            map[string]string{"app": "checkout"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       replicaSet.Name,
				UID:        replicaSet.UID,
				Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
}

func fakeClientWithObjects(t *testing.T, objects ...runtime.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
}

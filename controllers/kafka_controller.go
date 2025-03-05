/*
Copyright 2023.

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

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bigdatav1alpha1 "github.com/kubernetesbigdataeg/kafka-operator/api/v1alpha1"
)

const kafkaFinalizer = "bigdata.kubernetesbigdataeg.org/finalizer"

// Definitions to manage status conditions
const (
	// typeAvailableKafka represents the status of the Deployment reconciliation
	typeAvailableKafka = "Available"
	// typeDegradedKafka represents the status used when the custom resource is deleted and the finalizer operations are must to occur.
	typeDegradedKafka = "Degraded"
)

// KafkaReconciler reconciles a Kafka object
type KafkaReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// The following markers are used to generate the rules permissions (RBAC) on config/rbac using controller-gen
// when the command <make manifests> is executed.
// To know more about markers see: https://book.kubebuilder.io/reference/markers.html

//+kubebuilder:rbac:groups=bigdata.kubernetesbigdataeg.org,resources=kafkas,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bigdata.kubernetesbigdataeg.org,resources=kafkas/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=bigdata.kubernetesbigdataeg.org,resources=kafkas/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=configmaps;services,verbs=get;list;create;watch
//+kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.

// It is essential for the controller's reconciliation loop to be idempotent. By following the Operator
// pattern you will create Controllers which provide a reconcile function
// responsible for synchronizing resources until the desired state is reached on the cluster.
// Breaking this recommendation goes against the design principles of controller-runtime.
// and may lead to unforeseen consequences such as resources becoming stuck and requiring manual intervention.
// For further info:
// - About Operator Pattern: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
// - About Controllers: https://kubernetes.io/docs/concepts/architecture/controller/
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *KafkaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	//
	// 1. Control-loop: checking if Kafka CR exists
	//
	// Fetch the Kafka instance
	// The purpose is check if the Custom Resource for the Kind Kafka
	// is applied on the cluster if not we return nil to stop the reconciliation
	kafka := &bigdatav1alpha1.Kafka{}
	err := r.Get(ctx, req.NamespacedName, kafka)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If the custom resource is not found then, it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			log.Info("kafka resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get kafka")
		return ctrl.Result{}, err
	}

	//
	// 2. Control-loop: Status to Unknown
	//
	// Let's just set the status as Unknown when no status are available
	if kafka.Status.Conditions == nil || len(kafka.Status.Conditions) == 0 {
		meta.SetStatusCondition(&kafka.Status.Conditions, metav1.Condition{Type: typeAvailableKafka, Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: "Starting reconciliation"})
		if err = r.Status().Update(ctx, kafka); err != nil {
			log.Error(err, "Failed to update Kafka status")
			return ctrl.Result{}, err
		}

		// Let's re-fetch the kafka Custom Resource after update the status
		// so that we have the latest state of the resource on the cluster and we will avoid
		// raise the issue "the object has been modified, please apply
		// your changes to the latest version and try again" which would re-trigger the reconciliation
		// if we try to update it again in the following operations
		if err := r.Get(ctx, req.NamespacedName, kafka); err != nil {
			log.Error(err, "Failed to re-fetch kafka")
			return ctrl.Result{}, err
		}
	}

	//
	// 3. Control-loop: Let's add a finalizer
	//
	// Let's add a finalizer. Then, we can define some operations which should
	// occurs before the custom resource to be deleted.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers
	if !controllerutil.ContainsFinalizer(kafka, kafkaFinalizer) {
		log.Info("Adding Finalizer for Kafka")
		if ok := controllerutil.AddFinalizer(kafka, kafkaFinalizer); !ok {
			log.Error(err, "Failed to add finalizer into the custom resource")
			return ctrl.Result{Requeue: true}, nil
		}

		if err = r.Update(ctx, kafka); err != nil {
			log.Error(err, "Failed to update custom resource to add finalizer")
			return ctrl.Result{}, err
		}
	}

	//
	// 4. Control-loop: Instance marked for deletion
	//
	// Check if the Kafka instance is marked to be deleted, which is
	// indicated by the deletion timestamp being set.
	isKafkaMarkedToBeDeleted := kafka.GetDeletionTimestamp() != nil
	if isKafkaMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(kafka, kafkaFinalizer) {
			log.Info("Performing Finalizer Operations for Kafka before delete CR")

			// Let's add here an status "Downgrade" to define that this resource begin its process to be terminated.
			meta.SetStatusCondition(&kafka.Status.Conditions, metav1.Condition{Type: typeDegradedKafka,
				Status: metav1.ConditionUnknown, Reason: "Finalizing",
				Message: fmt.Sprintf("Performing finalizer operations for the custom resource: %s ", kafka.Name)})

			if err := r.Status().Update(ctx, kafka); err != nil {
				log.Error(err, "Failed to update Kafka status")
				return ctrl.Result{}, err
			}

			// Perform all operations required before remove the finalizer and allow
			// the Kubernetes API to remove the custom resource.
			r.doFinalizerOperationsForKafka(kafka)

			// TODO(user): If you add operations to the doFinalizerOperationsForKafka method
			// then you need to ensure that all worked fine before deleting and updating the Downgrade status
			// otherwise, you should requeue here.

			// Re-fetch the kafka Custom Resource before update the status
			// so that we have the latest state of the resource on the cluster and we will avoid
			// raise the issue "the object has been modified, please apply
			// your changes to the latest version and try again" which would re-trigger the reconciliation
			if err := r.Get(ctx, req.NamespacedName, kafka); err != nil {
				log.Error(err, "Failed to re-fetch kafka")
				return ctrl.Result{}, err
			}

			meta.SetStatusCondition(&kafka.Status.Conditions, metav1.Condition{Type: typeDegradedKafka,
				Status: metav1.ConditionTrue, Reason: "Finalizing",
				Message: fmt.Sprintf("Finalizer operations for custom resource %s name were successfully accomplished", kafka.Name)})

			if err := r.Status().Update(ctx, kafka); err != nil {
				log.Error(err, "Failed to update Kafka status")
				return ctrl.Result{}, err
			}

			log.Info("Removing Finalizer for Kafka after successfully perform the operations")
			if ok := controllerutil.RemoveFinalizer(kafka, kafkaFinalizer); !ok {
				log.Error(err, "Failed to remove finalizer for Kafka")
				return ctrl.Result{Requeue: true}, nil
			}

			if err := r.Update(ctx, kafka); err != nil {
				log.Error(err, "Failed to remove finalizer for Kafka")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	//
	// 5. Control-loop: Let's deploy/ensure our managed resources for Kafka
	// - Service ClusterIP,
	// - StateFulSet

	// Service: Check if the svc already exists, if not create a new one
	serviceFound := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: "kafka-svc", Namespace: kafka.Namespace}, serviceFound)
	if err != nil && apierrors.IsNotFound(err) {
		// Define the pdb resource
		svc, err := r.serviceForKafka(kafka)
		if err != nil {
			log.Error(err, "Failed to define new Service resource for Kafka")

			// The following implementation will update the status
			meta.SetStatusCondition(&kafka.Status.Conditions, metav1.Condition{Type: typeAvailableKafka,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("Failed to create Service for the custom resource (%s): (%s)",
					kafka.Name, err)})

			if err := r.Status().Update(ctx, kafka); err != nil {
				log.Error(err, "Failed to update Kafka status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		log.Info("Creating a new Service",
			"Service.Namespace", svc.Namespace, "Service.Name", svc.Name)

		if err = r.Create(ctx, svc); err != nil {
			log.Error(err, "Failed to create new Service",
				"Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			return ctrl.Result{}, err
		}

		// Service created successfully at this point.
		// We will requeue the reconciliation so that we can ensure the state
		// and move forward for the next operations
		//return ctrl.Result{RequeueAfter: time.Minute}, nil

	} else if err != nil {
		log.Error(err, "Failed to get Headless Service")
		// Let's return the error for the reconciliation be re-trigged again
		return ctrl.Result{}, err
	}

	// StateFulSet: Check if the sts already exists, if not create a new one
	found := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: kafka.Name, Namespace: kafka.Namespace}, found)
	if err != nil && apierrors.IsNotFound(err) {
		// Define a new sts
		dep, err := r.stateFulSetForKafka(kafka)
		if err != nil {
			log.Error(err, "Failed to define new StateFulSet resource for Kafka")

			// The following implementation will update the status
			meta.SetStatusCondition(&kafka.Status.Conditions, metav1.Condition{Type: typeAvailableKafka,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("Failed to create StatefulSet for the custom resource (%s): (%s)", kafka.Name, err)})

			if err := r.Status().Update(ctx, kafka); err != nil {
				log.Error(err, "Failed to update Kafka status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		log.Info("Creating a new StateFulSet",
			"StatefulSet.Namespace", dep.Namespace, "StatefulSet.Name", dep.Name)

		if err = r.Create(ctx, dep); err != nil {
			log.Error(err, "Failed to create new StatefulSet",
				"StatefulSet.Namespace", dep.Namespace, "StatefulSet.Name", dep.Name)
			return ctrl.Result{}, err
		}

		// StatefulSet created successfully at this point.
		// We will requeue the reconciliation so that we can ensure the state
		// and move forward for the next operations
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	} else if err != nil {
		log.Error(err, "Failed to get StatefulSet")
		// Let's return the error for the reconciliation be re-trigged again
		return ctrl.Result{}, err
	}

	//
	// 6. Control-loop: Check the number of replicas
	//
	// The CRD API is defining that the Kafka type, have a KafkaSpec.Size field
	// to set the quantity of Deployment instances is the desired state on the cluster.
	// Therefore, the following code will ensure the Deployment size is the same as defined
	// via the Size spec of the Custom Resource which we are reconciling.
	size := kafka.Spec.Size
	if *found.Spec.Replicas != size {
		found.Spec.Replicas = &size
		if err = r.Update(ctx, found); err != nil {
			log.Error(err, "Failed to update Deployment",
				"Deployment.Namespace", found.Namespace, "Deployment.Name", found.Name)

			// Re-fetch the kafka Custom Resource before update the status
			// so that we have the latest state of the resource on the cluster and we will avoid
			// raise the issue "the object has been modified, please apply
			// your changes to the latest version and try again" which would re-trigger the reconciliation
			if err := r.Get(ctx, req.NamespacedName, kafka); err != nil {
				log.Error(err, "Failed to re-fetch kafka")
				return ctrl.Result{}, err
			}

			// The following implementation will update the status
			meta.SetStatusCondition(&kafka.Status.Conditions, metav1.Condition{Type: typeAvailableKafka,
				Status: metav1.ConditionFalse, Reason: "Resizing",
				Message: fmt.Sprintf("Failed to update the size for the custom resource (%s): (%s)", kafka.Name, err)})

			if err := r.Status().Update(ctx, kafka); err != nil {
				log.Error(err, "Failed to update Kafka status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		// Now, that we update the size we want to requeue the reconciliation
		// so that we can ensure that we have the latest state of the resource before
		// update. Also, it will help ensure the desired state on the cluster
		return ctrl.Result{Requeue: true}, nil
	}

	//
	// 7. Control-loop: Let's update the status
	//
	// The following implementation will update the status
	meta.SetStatusCondition(&kafka.Status.Conditions, metav1.Condition{Type: typeAvailableKafka,
		Status: metav1.ConditionTrue, Reason: "Reconciling",
		Message: fmt.Sprintf("Deployment for custom resource (%s) with %d replicas created successfully", kafka.Name, size)})

	if err := r.Status().Update(ctx, kafka); err != nil {
		log.Error(err, "Failed to update Kafka status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// finalizeKafka will perform the required operations before delete the CR.
func (r *KafkaReconciler) doFinalizerOperationsForKafka(cr *bigdatav1alpha1.Kafka) {
	// TODO(user): Add the cleanup steps that the operator
	// needs to do before the CR can be deleted. Examples
	// of finalizers include performing backups and deleting
	// resources that are not owned by this CR, like a PVC.

	// Note: It is not recommended to use finalizers with the purpose of delete resources which are
	// created and managed in the reconciliation. These ones, such as the Deployment created on this reconcile,
	// are defined as depended of the custom resource. See that we use the method ctrl.SetControllerReference.
	// to set the ownerRef which means that the Deployment will be deleted by the Kubernetes API.
	// More info: https://kubernetes.io/docs/tasks/administer-cluster/use-cascading-deletion/

	// The following implementation will raise an event
	r.Recorder.Event(cr, "Warning", "Deleting",
		fmt.Sprintf("Custom Resource %s is being deleted from the namespace %s",
			cr.Name,
			cr.Namespace))
}

func (r *KafkaReconciler) serviceForKafka(
	v *bigdatav1alpha1.Kafka) (*corev1.Service, error) {

	labels := labels(v, "kafka")
	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kafka-svc",
			Namespace: v.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name: "kafka",
				Port: 9092,
			}},
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "None",
		},
	}

	if err := ctrl.SetControllerReference(v, s, r.Scheme); err != nil {
		return nil, err
	}

	return s, nil
}

// stateFulSetForKafka returns a Kafka StateFulSet object
func (r *KafkaReconciler) stateFulSetForKafka(
	kafka *bigdatav1alpha1.Kafka) (*appsv1.StatefulSet, error) {

	ls := labelsForKafka(kafka.Name)
	labels := labels(kafka, "kafka")

	replicas := kafka.Spec.Size

	// Get the Operand image
	image, err := imageForKafka()
	if err != nil {
		return nil, err
	}

	storageClass := "longhorn"

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kafka.Name,
			Namespace: kafka.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: "kafka-svc",
			Replicas:    &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type:          "RollingUpdate",
				RollingUpdate: nil,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Image:           image,
						Name:            "kafka",
						ImagePullPolicy: corev1.PullIfNotPresent,
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 9092,
								Name:          "kafka",
							},
						},
						Env: []corev1.EnvVar{
							{
								Name: "HOSTNAME_VALUE",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "status.podIP",
									},
								},
							},
							{
								Name:  "KAFKA_LISTENERS_COMMAND",
								Value: "echo \"PLAINTEXT://${HOSTNAME}.kafka-svc.default.svc.cluster.local:9092\"",
							},
							{
								Name:  "KAFKA_LOG4J_OPTS",
								Value: "-Dlog4j.configuration=file:/opt/kafka/config/log4j.properties",
							},
							{
								Name:  "BROKER_ID_COMMAND",
								Value: "echo ${HOSTNAME##*-}",
							},
							{
								Name:  "KAFKA_ZOOKEEPER_CONNECT",
								Value: "zookeeper-0.zookeeper-hs.default.svc.cluster.local:2181,zookeeper-1.zookeeper-hs.default.svc.cluster.local:2181,zookeeper-2.zookeeper-hs.default.svc.cluster.local:2181",
							},
							{
								Name:  "KAFKA_LOG_DIRS",
								Value: "/var/lib/kafka/data/topics",
							},
							{
								Name:  "KAFKA_LOG_RETENTION_HOURS",
								Value: "-1",
							},
							{
								Name:  "KAFAK_ZOOKEEPER_CONNECTION_TIMEOUT_MS",
								Value: "6000",
							},
							{
								Name:  "KAFAK_AUTO_CREATE_TOPICS_ENABLE",
								Value: "false",
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "data",
								MountPath: "/var/lib/kafka/data",
							},
						},
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "data",
					Labels: labels,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("150Mi"),
						},
					},
					StorageClassName: &storageClass,
				},
			}},
		},
	}

	// Set the ownerRef for the Deployment
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/
	if err := ctrl.SetControllerReference(kafka, dep, r.Scheme); err != nil {
		return nil, err
	}
	return dep, nil
}

// labelsForKafka returns the labels for selecting the resources
// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/common-labels/
func labelsForKafka(name string) map[string]string {
	var imageTag string
	image, err := imageForKafka()
	if err == nil {
		imageTag = strings.Split(image, ":")[1]
	}
	return map[string]string{"app.kubernetes.io/name": "Kafka",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/version":    imageTag,
		"app.kubernetes.io/part-of":    "kafka-operator",
		"app.kubernetes.io/created-by": "controller-manager",
		"app":                          "kafka",
	}
}

// imageForKafka gets the Operand image which is managed by this controller
// from the KAFKA_IMAGE environment variable defined in the config/manager/manager.yaml
func imageForKafka() (string, error) {
	/*var imageEnvVar = "KAFKA_IMAGE"
	image, found := os.LookupEnv(imageEnvVar)
	if !found {
		return "", fmt.Errorf("Unable to find %s environment variable with the image", imageEnvVar)
	}*/
	image := "docker.io/kubernetesbigdataeg/kafka:3.4.0-1"
	return image, nil
}

func labels(v *bigdatav1alpha1.Kafka, l string) map[string]string {
	return map[string]string{
		"app": l,
	}
}

// SetupWithManager sets up the controller with the Manager.
// Note that the Deployment will be also watched in order to ensure its
// desirable state on the cluster
func (r *KafkaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bigdatav1alpha1.Kafka{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

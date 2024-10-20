/*
Copyright 2024.

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

package controller

import (
	"bytes"
	"context"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/vvbogdanov87/acme-bucket/api/v1"
	cloudv1 "github.com/vvbogdanov87/acme-bucket/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// typeReadyBucket represents the status of the Bucket reconciliation
	typeReadyBucket = "Ready"

	bucketFinalizer = "bucket.cloud.acme.local/finalizer"
)

// BucketReconciler reconciles a Bucket object
type BucketReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cloud.acme.local,resources=buckets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloud.acme.local,resources=buckets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloud.acme.local,resources=buckets/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the Bucket instance
	bucketObj := &apiv1.Bucket{}
	if err := r.Get(ctx, req.NamespacedName, bucketObj); err != nil {
		log.Error(err, "unable to fetch Bucket")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Set the Ready condition to Flase when no status is available
	if len(bucketObj.Status.Conditions) == 0 {
		meta.SetStatusCondition(
			&bucketObj.Status.Conditions,
			metav1.Condition{
				Type:    typeReadyBucket,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: "Starting reconciliation",
			},
		)
		if err := r.Status().Update(ctx, bucketObj); err != nil {
			log.Error(err, "Failed to update Bucket status")
			return ctrl.Result{}, err
		}

		// Re-fetch the bucket Custom Resource after updating the status
		if err := r.Get(ctx, req.NamespacedName, bucketObj); err != nil {
			log.Error(err, "Failed to re-fetch bucket")
			return ctrl.Result{}, err
		}
	}

	// Add a finalizer
	if !controllerutil.ContainsFinalizer(bucketObj, bucketFinalizer) {
		log.Info("Adding Finalizer for Bucket")
		controllerutil.AddFinalizer(bucketObj, bucketFinalizer)

		if err := r.Update(ctx, bucketObj); err != nil {
			log.Error(err, "Failed to update custom resource to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Get the Pulumi program code
	program := createPulumiProgram(bucketObj.Name, bucketObj.Namespace)

	// Initialize the stack
	stackName := bucketObj.Namespace + "-" + bucketObj.Name

	project := auto.Project(workspace.Project{
		Name:    tokens.PackageName("Bucket"),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{
			URL: "s3://acme-cloud-backend",
		},
	})

	// Setup a passphrase secrets provider and use an environment variable to pass in the passphrase.
	secretsProvider := auto.SecretsProvider("passphrase")
	envvars := auto.EnvVars(map[string]string{
		// In a real program, you would feed in the password securely or via the actual environment.
		"PULUMI_CONFIG_PASSPHRASE": "password",
	})

	stackSettings := auto.Stacks(map[string]workspace.ProjectStack{
		stackName: {SecretsProvider: "passphrase"},
	})

	s, err := auto.UpsertStackInlineSource(
		ctx,
		stackName,
		"Bucket",
		program,
		project,
		secretsProvider,
		stackSettings,
		envvars,
	)
	if err != nil {
		// TODO: handle different error types
		return ctrl.Result{}, err
	}

	if err := s.SetConfig(ctx, "aws:region", auto.ConfigValue{Value: "us-west-2"}); err != nil {
		return ctrl.Result{}, err
	}

	// Check if the Bucket instance is marked to be deleted, which is
	// indicated by the deletion timestamp being set.
	if bucketObj.GetDeletionTimestamp() != nil {
		if controllerutil.ContainsFinalizer(bucketObj, bucketFinalizer) {
			log.Info("Performing Finalizer Operations for Bucket before delete CR")

			// Set the Ready condition to "False" to reflect that this resource began its process to be terminated.
			meta.SetStatusCondition(&bucketObj.Status.Conditions, metav1.Condition{
				Type:    typeReadyBucket,
				Status:  metav1.ConditionFalse,
				Reason:  "Finalizing",
				Message: fmt.Sprintf("Performing finalizer operations for the custom resource: %s ", bucketObj.Name),
			})

			if err := r.Status().Update(ctx, bucketObj); err != nil {
				log.Error(err, "Failed to update Bucket status")
				return ctrl.Result{}, err
			}

			// Delete the stack
			outBuf := new(bytes.Buffer)
			_, err := s.Destroy(ctx, optdestroy.ProgressStreams(outBuf))
			if err != nil {
				log.Error(err, "Failed to destroy stack")
				return ctrl.Result{}, nil
			}
			log.Info("successfully destroyed stack", s.Name(), outBuf.String())

			// Re-fetch the bucket Custom Resource before updating the status
			if err := r.Get(ctx, req.NamespacedName, bucketObj); err != nil {
				log.Error(err, "Failed to re-fetch bucket")
				return ctrl.Result{}, err
			}

			meta.SetStatusCondition(&bucketObj.Status.Conditions, metav1.Condition{
				Type:   typeReadyBucket,
				Status: metav1.ConditionFalse,
				Reason: "Finalizing",
				Message: fmt.Sprintf(
					"Finalizer operations for custom resource %s name were successfully accomplished",
					bucketObj.Name,
				),
			})

			if err := r.Status().Update(ctx, bucketObj); err != nil {
				log.Error(err, "Failed to update Bucket status")
				return ctrl.Result{}, err
			}

			log.Info("Removing Finalizer for Bucket after successfully perform the operations")
			if ok := controllerutil.RemoveFinalizer(bucketObj, bucketFinalizer); !ok {
				log.Error(err, "Failed to remove finalizer for Bucket")
				return ctrl.Result{Requeue: true}, nil
			}

			if err := r.Update(ctx, bucketObj); err != nil {
				log.Error(err, "Failed to remove finalizer for Bucket")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Create or update the stack
	// we'll write all of the update logs to a buffer
	outBuf := new(bytes.Buffer)
	upRes, err := s.Up(ctx, optup.ProgressStreams(outBuf))
	if err != nil {
		log.Error(err, "failed to deploy stack", s.Name(), outBuf.String())
		return ctrl.Result{}, err
	}
	log.Info("successfully deployed/updated stack", s.Name(), outBuf.String())

	bucketObj.Status.Arn = upRes.Outputs["arn"].Value.(string)
	if err = r.Status().Update(ctx, bucketObj); err != nil {
		log.Error(err, "Failed to update Bucket status")
		return ctrl.Result{}, err
	}

	// Set the Ready condtion to "True" to reflect that this resource is created.
	meta.SetStatusCondition(&bucketObj.Status.Conditions, metav1.Condition{
		Type:    typeReadyBucket,
		Status:  metav1.ConditionTrue,
		Reason:  "Created",
		Message: "The Bucket was successfully created",
	})

	if err := r.Status().Update(ctx, bucketObj); err != nil {
		log.Error(err, "Failed to update Bucket status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudv1.Bucket{}).
		Complete(r)
}

func createPulumiProgram(name, namespace string) pulumi.RunFunc {
	return func(ctx *pulumi.Context) error {
		bucket, err := s3.NewBucketV2(ctx, "bucket", &s3.BucketV2Args{
			Bucket: pulumi.String(name),
			Tags: pulumi.StringMap{
				"Name":        pulumi.String(name),
				"Environment": pulumi.String(namespace),
			},
		})
		if err != nil {
			return err
		}

		// export the bucket ARN
		ctx.Export("arn", bucket.Arn)
		return nil
	}
}

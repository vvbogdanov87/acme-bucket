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

package v1

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/vvbogdanov87/acme-common/pkg/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// BucketSpec defines the desired state of Bucket
type BucketSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Tags map[string]string `json:"tags,omitempty"`
}

// BucketStatus defines the observed state of Bucket
type BucketStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Arn string `json:"arn,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Arn",type=string,JSONPath=`.status.arn`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type == 'Ready')].status`

// Bucket is the Schema for the buckets API
type Bucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketSpec   `json:"spec,omitempty"`
	Status BucketStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketList contains a list of Bucket
type BucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bucket `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Bucket{}, &BucketList{})
}

// Ensure the implementation satisfies the interfaces
var _ controller.Obj = &Bucket{}

func (b *Bucket) GetStatusConditions() *[]metav1.Condition {
	return &b.Status.Conditions
}

func (b *Bucket) SetStatus(output auto.OutputMap) {
	b.Status.Arn = output["arn"].Value.(string)
}

func (b *Bucket) GetPulumiProgram() pulumi.RunFunc {
	return func(ctx *pulumi.Context) error {
		bucketTags := pulumi.StringMap{}
		bucketTags["Name"] = pulumi.String(b.ObjectMeta.Name)
		for k, v := range b.Spec.Tags {
			bucketTags[k] = pulumi.String(v)
		}

		bucket, err := s3.NewBucketV2(ctx, "bucket", &s3.BucketV2Args{
			Bucket: pulumi.String(b.ObjectMeta.Name),
			Tags:   bucketTags,
		})
		if err != nil {
			return err
		}

		_, err = s3.NewBucketPublicAccessBlock(ctx, "bucket_pub_access", &s3.BucketPublicAccessBlockArgs{
			Bucket:                bucket.ID(),
			BlockPublicAcls:       pulumi.Bool(true),
			BlockPublicPolicy:     pulumi.Bool(true),
			IgnorePublicAcls:      pulumi.Bool(true),
			RestrictPublicBuckets: pulumi.Bool(true),
		})
		if err != nil {
			return err
		}

		_, err = s3.NewBucketServerSideEncryptionConfigurationV2(ctx, "bucket_sse_config", &s3.BucketServerSideEncryptionConfigurationV2Args{
			Bucket: bucket.ID(),
			Rules: s3.BucketServerSideEncryptionConfigurationV2RuleArray{
				&s3.BucketServerSideEncryptionConfigurationV2RuleArgs{
					ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs{
						SseAlgorithm: pulumi.String("aws:kms"),
					},
				},
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

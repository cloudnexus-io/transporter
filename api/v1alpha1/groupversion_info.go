package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is group version used to register these objects
var GroupVersion = schema.GroupVersion{
	Group:   "migration.transporter.io",
	Version: "v1alpha1",
}

var (
	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	// Register the types with the Scheme so the components can map objects to GroupVersionKinds and back
	SchemeBuilder.Register(&PodMigration{}, &PodMigrationList{})
}


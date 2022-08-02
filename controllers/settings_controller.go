package controllers

import (
	"context"
	"time"

	"github.com/kcp-dev/logicalcluster"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	cutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	settingsv1alpha1 "github.com/fgiloux/settings-controller/api/v1alpha1"
)

// SettingsReconciler reconciles a Settings object
type SettingsReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=resourcequotas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=resourcequotas/finalizers,verbs=update

// +kubebuilder:rbac:groups=configuration.pipeline-service.io,resources=settings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=configuration.pipeline-service.io,resources=settings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=configuration.pipeline-service.io,resources=settings/finalizers,verbs=update

func (r *SettingsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Include the clusterName from req.ObjectKey in the logger, similar to the namespace and name keys that are already
	// there.
	logger = logger.WithValues("clusterName", req.ClusterName)

	// TODO: to be removed
	// You probably wouldn't need to do this, but if you wanted to list all instances across all logical clusters:
	var allSettings settingsv1alpha1.SettingsList
	if err := r.List(ctx, &allSettings); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Listed all Settings across all workspaces", "count", len(allSettings.Items))

	// Add the logical cluster to the context
	ctx = logicalcluster.WithCluster(ctx, logicalcluster.New(req.ClusterName))

	logger.Info("Getting Settings")
	var s settingsv1alpha1.Settings
	if err := r.Get(ctx, req.NamespacedName, &s); err != nil {
		if errors.IsNotFound(err) {
			// Normal - was deleted
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	quotaCondition := metav1.Condition{
		Type:   "QuotaReady",
		Status: metav1.ConditionTrue,
		LastTransitionTime: metav1.Time{
			Time: time.Now().UTC(),
		},
		Reason:  "QuotaCreated",
		Message: "ResourceQuota pipeline-service has successfully been created in kcp-system namespace",
	}
	scopy := s.DeepCopy()
	conditionNew := true
	conditionChanged := false
	var rtnErr error

	var wsQuota corev1.ResourceQuota
	wsQuota.SetNamespace("kcp-system")
	wsQuota.SetName("quota")

	// TODO: hardcoded quota for now, should be provided by ComponentConfig
	operationResult, rtnErr := cutil.CreateOrPatch(ctx, r.Client, &wsQuota, func() error {
		wsQuota.Spec = corev1.ResourceQuotaSpec{
			Hard: map[corev1.ResourceName]resource.Quantity{
				"count/namespace": resource.MustParse("10"),
			},
		}
		return nil
	})
	if rtnErr != nil {
		logger.Error(rtnErr, "unable to create or patch the resourceQuota")
		quotaCondition.Status = metav1.ConditionFalse
		quotaCondition.Reason = "Error"
		quotaCondition.Message = "Unable to create or patch the resourceQuota"
	}
	logger.Info(string(operationResult), "resourceQuota", wsQuota.GetName())

	// Update the condition only if it is missing or the status of the available condition has changed.
	// TODO: it would be good to set condition to unknown at the very beginning of the processing when none has been defined and to reloop
	for i, condition := range s.Status.Conditions {
		if condition.Type == quotaCondition.Type {
			conditionNew = false
			if condition.Status != quotaCondition.Status || condition.Reason != quotaCondition.Reason {
				s.Status.Conditions[i] = quotaCondition
				conditionChanged = true
				break
			}
		}
	}
	if conditionNew {
		s.Status.Conditions = append(s.Status.Conditions, quotaCondition)
		conditionChanged = true
	}

	if conditionChanged {
		logger.Info("Patching Settings status to store the new condition(s) in the current logical cluster")
		patch := client.MergeFrom(scopy)

		if err := r.Status().Patch(ctx, &s, patch); err != nil {
			logger.Info("Patch error", "error", err)
			// TODO: depending on the error I may just give up
			if rtnErr == nil {
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, rtnErr
}

// SetupWithManager sets up the controller with the Manager.
func (r *SettingsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&settingsv1alpha1.Settings{}).
		Owns(&corev1.ResourceQuota{}).
		Complete(r)
}
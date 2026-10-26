package controller

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

// ---------- NamespacesOptedIn condition (#261) ----------
//
// Contract under test: the condition is informational. It is False with
// the opt-in command while nothing matches the selector, True with the
// count once something does, it follows the snapshot in effect (so it is
// right when the spec is invalid and defaults apply), and it never
// touches Ready or Degraded. Events fire on transitions only.

func labeledNamespace(name string, lbls map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}
}

func optedInCondition(t *testing.T, cr *aibomv1beta1.AIBOMControllerConfig) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(cr.Status.Conditions, aibomv1beta1.AIBOMControllerConfigConditionNamespacesOptedIn)
	if c == nil {
		t.Fatalf("NamespacesOptedIn condition missing; have %+v", cr.Status.Conditions)
	}
	return c
}

func TestNamespacesOptedIn_NoneMatch_FalseWithCommand(t *testing.T) {
	r, rec := newDirectReconciler(t, validCR(),
		labeledNamespace("default", nil),
		labeledNamespace("kube-system", map[string]string{"kubernetes.io/metadata.name": "kube-system"}),
	)
	reconcileOnce(t, r)

	got := getCR(t, r.Client, config.DefaultConfigName)
	c := optedInCondition(t, got)
	if c.Status != metav1.ConditionFalse || c.Reason != aibomv1beta1.ReasonNoNamespaceMatchesSelector {
		t.Fatalf("status/reason = %s/%s, want False/%s", c.Status, c.Reason, aibomv1beta1.ReasonNoNamespaceMatchesSelector)
	}
	wantCmd := "kubectl label namespace <name> " + config.DefaultNamespaceOptInLabel + "=true"
	if !strings.Contains(c.Message, wantCmd) {
		t.Errorf("message %q does not carry the opt-in command %q", c.Message, wantCmd)
	}
	// Ready is untouched: nothing opted in is a healthy install.
	assertCondition(t, got, aibomv1beta1.AIBOMControllerConfigConditionReady, metav1.ConditionTrue, aibomv1beta1.ReasonConfigLoaded)
	assertCondition(t, got, aibomv1beta1.AIBOMControllerConfigConditionDegraded, metav1.ConditionFalse, aibomv1beta1.ReasonConfigLoaded)

	evs := rec.eventsWithReason(EventReasonNoNamespacesOptedIn)
	if len(evs) != 1 {
		t.Fatalf("NoNamespacesOptedIn events = %d, want 1", len(evs))
	}
	if evs[0].EventType != corev1.EventTypeNormal {
		t.Errorf("event type = %s, want Normal (informational, never Warning)", evs[0].EventType)
	}
	if o, ok := evs[0].Object.(*corev1.ObjectReference); !ok || o.Kind != "AIBOMControllerConfig" {
		t.Errorf("event target = %T %+v, want the AIBOMControllerConfig", evs[0].Object, evs[0].Object)
	}

	// A second reconcile with no change: condition stays, no second event.
	reconcileOnce(t, r)
	if n := len(rec.eventsWithReason(EventReasonNoNamespacesOptedIn)); n != 1 {
		t.Errorf("NoNamespacesOptedIn events after steady reconcile = %d, want 1 (transitions only)", n)
	}
}

func TestNamespacesOptedIn_LabeledNamespaces_TrueWithCount(t *testing.T) {
	r, rec := newDirectReconciler(t, validCR(),
		labeledNamespace("default", nil),
		labeledNamespace("ml-a", map[string]string{config.DefaultNamespaceOptInLabel: "true"}),
		labeledNamespace("ml-b", map[string]string{config.DefaultNamespaceOptInLabel: "true"}),
		labeledNamespace("ml-off", map[string]string{config.DefaultNamespaceOptInLabel: "false"}),
	)
	reconcileOnce(t, r)

	got := getCR(t, r.Client, config.DefaultConfigName)
	c := optedInCondition(t, got)
	if c.Status != metav1.ConditionTrue || c.Reason != aibomv1beta1.ReasonNamespacesMatchSelector {
		t.Fatalf("status/reason = %s/%s, want True/%s", c.Status, c.Reason, aibomv1beta1.ReasonNamespacesMatchSelector)
	}
	if !strings.HasPrefix(c.Message, "2 namespaces match") {
		t.Errorf("message = %q, want count of 2 (ml-off has the label set to false)", c.Message)
	}
	if n := len(rec.eventsWithReason(EventReasonNamespacesOptedIn)); n != 1 {
		t.Errorf("NamespacesOptedIn events = %d, want 1", n)
	}
	if n := len(rec.eventsWithReason(EventReasonNoNamespacesOptedIn)); n != 0 {
		t.Errorf("NoNamespacesOptedIn events = %d, want 0", n)
	}
}

func TestNamespacesOptedIn_TerminatingNamespaceNotCounted(t *testing.T) {
	ns := labeledNamespace("ml-gone", map[string]string{config.DefaultNamespaceOptInLabel: "true"})
	ns.Status.Phase = corev1.NamespaceTerminating
	r, _ := newDirectReconciler(t, validCR(), ns)
	reconcileOnce(t, r)

	c := optedInCondition(t, getCR(t, r.Client, config.DefaultConfigName))
	if c.Status != metav1.ConditionFalse {
		t.Errorf("status = %s, want False: a Terminating namespace is not inventoried", c.Status)
	}
}

func TestNamespacesOptedIn_CustomSelector_MessageNamesSelector(t *testing.T) {
	cr := validCR()
	cr.Spec.Discovery.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"team": "ml"},
	}
	r, _ := newDirectReconciler(t, cr,
		labeledNamespace("ml-a", map[string]string{config.DefaultNamespaceOptInLabel: "true"}), // default label, not the custom one
	)
	reconcileOnce(t, r)

	c := optedInCondition(t, getCR(t, r.Client, config.DefaultConfigName))
	if c.Status != metav1.ConditionFalse {
		t.Fatalf("status = %s, want False (custom selector team=ml matches nothing)", c.Status)
	}
	if !strings.Contains(c.Message, "team=ml") {
		t.Errorf("message %q should name the selector in effect", c.Message)
	}
	if strings.Contains(c.Message, "kubectl label namespace <name> "+config.DefaultNamespaceOptInLabel) {
		t.Errorf("message %q must not suggest the default label when a custom selector is in effect", c.Message)
	}
}

func TestNamespacesOptedIn_InvalidSpec_FollowsSnapshotInEffect(t *testing.T) {
	// Invalid spec on a fresh start: the controller runs on defaults, so
	// the default selector is what decides the count. Ready=False is the
	// spec's problem; NamespacesOptedIn still reports accurately.
	r, _ := newDirectReconciler(t, invalidCR(),
		labeledNamespace("ml-a", map[string]string{config.DefaultNamespaceOptInLabel: "true"}),
	)
	reconcileOnce(t, r)

	got := getCR(t, r.Client, config.DefaultConfigName)
	assertCondition(t, got, aibomv1beta1.AIBOMControllerConfigConditionReady, metav1.ConditionFalse, aibomv1beta1.ReasonConfigInvalid)
	c := optedInCondition(t, got)
	if c.Status != metav1.ConditionTrue || !strings.HasPrefix(c.Message, "1 namespace matches") {
		t.Errorf("condition = %s %q, want True with count 1 under the default selector", c.Status, c.Message)
	}
}

func TestNamespacesOptedIn_TransitionEventsFireOncePerChange(t *testing.T) {
	r, rec := newDirectReconciler(t, validCR(), labeledNamespace("default", nil))
	reconcileOnce(t, r)
	if n := len(rec.eventsWithReason(EventReasonNoNamespacesOptedIn)); n != 1 {
		t.Fatalf("initial NoNamespacesOptedIn events = %d, want 1", n)
	}

	// Opt a namespace in → one NamespacesOptedIn event.
	ns := labeledNamespace("ml-a", map[string]string{config.DefaultNamespaceOptInLabel: "true"})
	if err := r.Create(t.Context(), ns); err != nil {
		t.Fatalf("create ns: %v", err)
	}
	reconcileOnce(t, r)
	reconcileOnce(t, r)
	if n := len(rec.eventsWithReason(EventReasonNamespacesOptedIn)); n != 1 {
		t.Errorf("NamespacesOptedIn events = %d, want 1 after two reconciles", n)
	}

	// Remove the label → back to False, second NoNamespacesOptedIn event.
	ns.Labels = nil
	if err := r.Update(t.Context(), ns); err != nil {
		t.Fatalf("update ns: %v", err)
	}
	reconcileOnce(t, r)
	if n := len(rec.eventsWithReason(EventReasonNoNamespacesOptedIn)); n != 2 {
		t.Errorf("NoNamespacesOptedIn events = %d, want 2", n)
	}
	c := optedInCondition(t, getCR(t, r.Client, config.DefaultConfigName))
	if c.Status != metav1.ConditionFalse {
		t.Errorf("status = %s, want False after label removed", c.Status)
	}
}

// Envtest: a namespace label change, with no change to the CR, must
// re-evaluate the condition through the namespace watch wired in
// SetupWithManager. This is the path a platform admin exercises when
// they run the command from the message.
func TestReconcileEnvtest_NamespaceLabelFlipsOptedIn(t *testing.T) {
	env, _, _ := startConfigEnvTest(t)

	if err := env.k8sClient.Create(env.mgrCtx, validCR()); err != nil {
		t.Fatalf("create CR: %v", err)
	}
	waitOptedIn := func(want metav1.ConditionStatus) {
		t.Helper()
		eventually(t, 15*time.Second, 100*time.Millisecond, func() error {
			var got aibomv1beta1.AIBOMControllerConfig
			if err := env.k8sClient.Get(env.mgrCtx, types.NamespacedName{Name: config.DefaultConfigName}, &got); err != nil {
				return err
			}
			c := meta.FindStatusCondition(got.Status.Conditions, aibomv1beta1.AIBOMControllerConfigConditionNamespacesOptedIn)
			if c == nil {
				return errNotYet("condition not set yet")
			}
			if c.Status != want {
				return errNotYet("NamespacesOptedIn=" + string(c.Status) + ", want " + string(want))
			}
			return nil
		})
	}
	// Fresh envtest: default/kube-* namespaces only, none labeled.
	waitOptedIn(metav1.ConditionFalse)

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ml-flip"}}
	if err := env.k8sClient.Create(env.mgrCtx, ns); err != nil {
		t.Fatalf("create ns: %v", err)
	}
	// Creating an unlabeled namespace must not flip it.
	time.Sleep(500 * time.Millisecond)
	waitOptedIn(metav1.ConditionFalse)

	ns.Labels = map[string]string{config.DefaultNamespaceOptInLabel: "true"}
	if err := env.k8sClient.Update(env.mgrCtx, ns); err != nil {
		t.Fatalf("label ns: %v", err)
	}
	waitOptedIn(metav1.ConditionTrue)

	ns.Labels = nil
	if err := env.k8sClient.Update(env.mgrCtx, ns); err != nil {
		t.Fatalf("unlabel ns: %v", err)
	}
	waitOptedIn(metav1.ConditionFalse)
}

type errNotYet string

func (e errNotYet) Error() string { return string(e) }

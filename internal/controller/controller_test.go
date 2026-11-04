package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/openshift/hypershift/api/hypershift/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type countingTokenClient struct {
	mu    sync.Mutex
	calls int
}

func (c *countingTokenClient) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	token := base64.StdEncoding.EncodeToString([]byte("AWS:secret"))
	return &ecr.GetAuthorizationTokenOutput{AuthorizationData: []ecrtypes.AuthorizationData{{AuthorizationToken: &token}}}, nil
}

func (c *countingTokenClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// emptyTokenClient simulates ECR returning an empty AuthorizationData slice.
type emptyTokenClient struct{}

func (e *emptyTokenClient) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	return &ecr.GetAuthorizationTokenOutput{AuthorizationData: []ecrtypes.AuthorizationData{}}, nil
}

func TestRegistryFromRepository(t *testing.T) {
	registry, err := registryFromRepository("123456789012.dkr.ecr.us-east-1.amazonaws.com/rosa/release")
	if err != nil || registry != "123456789012.dkr.ecr.us-east-1.amazonaws.com" {
		t.Fatalf("registry = %q, err = %v", registry, err)
	}
	if _, err := registryFromRepository("not-a-repository"); err == nil {
		t.Fatal("expected repository without a path to fail")
	}
}

func TestAddECR(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("AWS:secret"))
	var m mergedAuths
	m.addECR("123456789012.dkr.ecr.us-east-1.amazonaws.com", ecrtypes.AuthorizationData{AuthorizationToken: &token})
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(m.bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if config.Auths["123456789012.dkr.ecr.us-east-1.amazonaws.com"].Auth != token {
		t.Fatalf("unexpected auth config: %s", m.bytes())
	}
}

func TestAccountFromRegistry(t *testing.T) {
	if got := accountFromRegistry("123456789012.dkr.ecr.us-east-1.amazonaws.com"); got != "123456789012" {
		t.Fatalf("account = %q", got)
	}
	if got := accountFromRegistry("public.ecr.aws"); got != "" {
		t.Fatalf("account = %q", got)
	}
}

func TestMergedAuths_MergeAndAddECR(t *testing.T) {
	// Build a source dockerconfigjson with one registry
	sourceAuth := map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("user:pass"))}
	sourceEntry, _ := json.Marshal(sourceAuth)
	sourceCfg, _ := json.Marshal(map[string]any{"auths": map[string]json.RawMessage{
		"source-registry.example.com": sourceEntry,
	}})

	var m mergedAuths
	if err := m.merge(sourceCfg); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// Add ECR credentials
	token := base64.StdEncoding.EncodeToString([]byte("AWS:ecr-token"))
	m.addECR("123456789012.dkr.ecr.us-east-1.amazonaws.com", ecrtypes.AuthorizationData{AuthorizationToken: &token})

	var result struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(m.bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := result.Auths["source-registry.example.com"]; !ok {
		t.Error("source registry missing from merged result")
	}
	if _, ok := result.Auths["123456789012.dkr.ecr.us-east-1.amazonaws.com"]; !ok {
		t.Error("ECR registry missing from merged result")
	}
}

func TestMergedAuths_EmptySourceProducesECROnly(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("AWS:ecr-token"))
	var m mergedAuths
	m.addECR("123456789012.dkr.ecr.us-east-1.amazonaws.com", ecrtypes.AuthorizationData{AuthorizationToken: &token})

	var result struct {
		Auths map[string]any `json:"auths"`
	}
	if err := json.Unmarshal(m.bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Auths) != 1 {
		t.Errorf("expected 1 auth entry, got %d", len(result.Auths))
	}
}

func TestMergedAuths_ECROverwritesExistingEntry(t *testing.T) {
	// Pre-existing ECR entry in source
	oldToken := base64.StdEncoding.EncodeToString([]byte("old-token"))
	oldEntry, _ := json.Marshal(map[string]string{"auth": oldToken})
	sourceCfg, _ := json.Marshal(map[string]any{"auths": map[string]json.RawMessage{
		"123456789012.dkr.ecr.us-east-1.amazonaws.com": oldEntry,
	}})

	var m mergedAuths
	_ = m.merge(sourceCfg)

	newToken := base64.StdEncoding.EncodeToString([]byte("AWS:new-token"))
	m.addECR("123456789012.dkr.ecr.us-east-1.amazonaws.com", ecrtypes.AuthorizationData{AuthorizationToken: &newToken})

	var result struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	_ = json.Unmarshal(m.bytes(), &result)
	if result.Auths["123456789012.dkr.ecr.us-east-1.amazonaws.com"].Auth != newToken {
		t.Error("expected new ECR token to overwrite old entry")
	}
}

func TestMergedAuths_MergeMalformedJSONReturnsError(t *testing.T) {
	var m mergedAuths
	if err := m.merge([]byte("not-json")); err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

// newTestScheme builds a runtime.Scheme with the types needed for fake client tests.
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1beta1.AddToScheme(s)
	return s
}

// newFakeReconciler returns a Reconciler backed by a fake Kubernetes client and
// a counting ECR token client for use in reconcile-level unit tests.
func newFakeReconciler(scheme *runtime.Scheme, objs ...runtime.Object) (*Reconciler, *countingTokenClient) {
	ecrClient := &countingTokenClient{}
	token := base64.StdEncoding.EncodeToString([]byte("AWS:test-token"))
	ecrClient.calls = 0
	_ = token // countingTokenClient always returns a fixed token

	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, o := range objs {
		builder = builder.WithRuntimeObjects(o)
	}

	return &Reconciler{
		Client:       builder.Build(),
		ECR:          ecrClient,
		Repository:   "123456789012.dkr.ecr.us-east-1.amazonaws.com/example-repo",
		RefreshAfter: time.Hour,
	}, ecrClient
}

func TestReconcile_WritesECRCredentialsToPullSecret(t *testing.T) {
	scheme := newTestScheme(t)
	hc := &v1beta1.HostedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "test-ns"},
		Spec:       v1beta1.HostedClusterSpec{PullSecret: corev1.LocalObjectReference{Name: "pull-secret"}},
	}
	r, _ := newFakeReconciler(scheme, hc)

	_, err := r.Reconcile(context.Background(), nn("test-ns", "test-cluster"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), objKey("test-ns", "pull-secret"), secret); err != nil {
		t.Fatalf("get pull-secret: %v", err)
	}
	var cfg struct {
		Auths map[string]any `json:"auths"`
	}
	if err := json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &cfg); err != nil {
		t.Fatalf("unmarshal pull-secret: %v", err)
	}
	if _, ok := cfg.Auths["123456789012.dkr.ecr.us-east-1.amazonaws.com"]; !ok {
		t.Error("ECR registry missing from pull-secret auths")
	}
}

func TestReconcile_MergesSourceSecretCredentials(t *testing.T) {
	scheme := newTestScheme(t)

	// Source secret with quay.io credentials
	sourceAuth := base64.StdEncoding.EncodeToString([]byte("user:pass"))
	sourceEntry, _ := json.Marshal(map[string]string{"auth": sourceAuth})
	sourceCfg, _ := json.Marshal(map[string]any{"auths": map[string]json.RawMessage{
		"source-registry.example.com": sourceEntry,
	}})
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "base-pull-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: sourceCfg},
	}
	hc := &v1beta1.HostedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "test-ns"},
		Spec:       v1beta1.HostedClusterSpec{PullSecret: corev1.LocalObjectReference{Name: "pull-secret"}},
	}
	r, _ := newFakeReconciler(scheme, hc, sourceSecret)
	r.SourceSecrets = []string{"base-pull-secret"}

	_, err := r.Reconcile(context.Background(), nn("test-ns", "test-cluster"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), objKey("test-ns", "pull-secret"), secret); err != nil {
		t.Fatalf("get pull-secret: %v", err)
	}
	var cfg struct {
		Auths map[string]any `json:"auths"`
	}
	if err := json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := cfg.Auths["source-registry.example.com"]; !ok {
		t.Error("source registry credentials missing from merged pull-secret")
	}
	if _, ok := cfg.Auths["123456789012.dkr.ecr.us-east-1.amazonaws.com"]; !ok {
		t.Error("ECR credentials missing from merged pull-secret")
	}
}

func TestReconcile_ContinuesWhenSourceSecretMissing(t *testing.T) {
	scheme := newTestScheme(t)
	hc := &v1beta1.HostedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "test-ns"},
		Spec:       v1beta1.HostedClusterSpec{PullSecret: corev1.LocalObjectReference{Name: "pull-secret"}},
	}
	r, _ := newFakeReconciler(scheme, hc)
	r.SourceSecrets = []string{"missing-secret"} // does not exist

	_, err := r.Reconcile(context.Background(), nn("test-ns", "test-cluster"))
	if err != nil {
		t.Fatalf("Reconcile should continue when source secret is missing, got: %v", err)
	}

	// pull-secret should still be written with ECR-only credentials
	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), objKey("test-ns", "pull-secret"), secret); err != nil {
		t.Fatalf("get pull-secret: %v", err)
	}
	if len(secret.Data[corev1.DockerConfigJsonKey]) == 0 {
		t.Error("expected pull-secret to have ECR credentials even when source secret is missing")
	}
}

func nn(ns, name string) reconcile.Request {
	return reconcile.Request{NamespacedName: k8stypes.NamespacedName{Namespace: ns, Name: name}}
}

func objKey(ns, name string) client.ObjectKey {
	return client.ObjectKey{Namespace: ns, Name: name}
}

func TestReconcile_MalformedSourceSecretIsSkipped(t *testing.T) {
	scheme := newTestScheme(t)
	malformed := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte("not-json")},
	}
	hc := &v1beta1.HostedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "test-ns"},
		Spec:       v1beta1.HostedClusterSpec{PullSecret: corev1.LocalObjectReference{Name: "pull-secret"}},
	}
	r, _ := newFakeReconciler(scheme, hc, malformed)
	r.SourceSecrets = []string{"bad-secret"}

	// Should not return an error — malformed source is skipped, ECR creds still written.
	_, err := r.Reconcile(context.Background(), nn("test-ns", "test-cluster"))
	if err != nil {
		t.Fatalf("Reconcile should skip malformed source secret, got: %v", err)
	}

	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), objKey("test-ns", "pull-secret"), secret); err != nil {
		t.Fatalf("get pull-secret: %v", err)
	}
	var cfg struct {
		Auths map[string]any `json:"auths"`
	}
	if err := json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := cfg.Auths["123456789012.dkr.ecr.us-east-1.amazonaws.com"]; !ok {
		t.Error("ECR credentials missing — skipping malformed source should not prevent ECR write")
	}
}

func TestReconcile_SourceSecretWithNoDockerconfigjsonKeyIsNoOp(t *testing.T) {
	scheme := newTestScheme(t)
	// Secret exists but has no .dockerconfigjson key
	emptySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{},
	}
	hc := &v1beta1.HostedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "test-ns"},
		Spec:       v1beta1.HostedClusterSpec{PullSecret: corev1.LocalObjectReference{Name: "pull-secret"}},
	}
	r, _ := newFakeReconciler(scheme, hc, emptySecret)
	r.SourceSecrets = []string{"empty-secret"}

	_, err := r.Reconcile(context.Background(), nn("test-ns", "test-cluster"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), objKey("test-ns", "pull-secret"), secret); err != nil {
		t.Fatalf("get pull-secret: %v", err)
	}
	// ECR credentials should still be written
	if len(secret.Data[corev1.DockerConfigJsonKey]) == 0 {
		t.Error("expected ECR credentials to be written even when source secret has no dockerconfigjson")
	}
}

func TestReconcile_HostedClusterNotFound_ReturnsNil(t *testing.T) {
	scheme := newTestScheme(t)
	r, _ := newFakeReconciler(scheme) // no HostedCluster in fake client

	result, err := r.Reconcile(context.Background(), nn("test-ns", "nonexistent"))
	if err != nil {
		t.Fatalf("expected nil error for not-found HC, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty result for not-found HC, got: %v", result)
	}
}

func TestReconcile_HostedClusterBeingDeleted_ReturnsNil(t *testing.T) {
	scheme := newTestScheme(t)
	now := metav1.Now()
	hc := &v1beta1.HostedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-cluster",
			Namespace:         "test-ns",
			DeletionTimestamp: &now,
			Finalizers:        []string{"test-finalizer"}, // required for deletion timestamp to be set
		},
		Spec: v1beta1.HostedClusterSpec{PullSecret: corev1.LocalObjectReference{Name: "pull-secret"}},
	}
	r, _ := newFakeReconciler(scheme, hc)

	result, err := r.Reconcile(context.Background(), nn("test-ns", "test-cluster"))
	if err != nil {
		t.Fatalf("expected nil error for deleting HC, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty result for deleting HC, got: %v", result)
	}
}

func TestReconcile_EmptyPullSecretName_ReturnsError(t *testing.T) {
	scheme := newTestScheme(t)
	hc := &v1beta1.HostedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "test-ns"},
		Spec:       v1beta1.HostedClusterSpec{}, // PullSecret.Name is empty
	}
	r, _ := newFakeReconciler(scheme, hc)

	_, err := r.Reconcile(context.Background(), nn("test-ns", "test-cluster"))
	if err == nil {
		t.Fatal("expected error when spec.pullSecret.name is empty")
	}
}

func TestFetchAuthorization_EmptyResponseReturnsError(t *testing.T) {
	r := &Reconciler{
		ECR:          &emptyTokenClient{},
		Repository:   "123456789012.dkr.ecr.us-east-1.amazonaws.com/example-repo",
		RefreshAfter: time.Hour,
	}
	_, err := r.fetchAuthorization(context.Background(), "123456789012.dkr.ecr.us-east-1.amazonaws.com")
	if err == nil {
		t.Fatal("expected error when ECR returns empty AuthorizationData")
	}
}

func TestMergedAuths_MergeNilDataIsNoOp(t *testing.T) {
	var m mergedAuths
	if err := m.merge(nil); err != nil {
		t.Errorf("merge(nil) should be a no-op, got error: %v", err)
	}
	if m.auths != nil {
		t.Errorf("merge(nil) should leave auths nil, got %v", m.auths)
	}
}

func TestAuthorizationUsesSharedTokenUntilRefresh(t *testing.T) {
	ecrClient := &countingTokenClient{}
	reconciler := &Reconciler{ECR: ecrClient, RefreshAfter: time.Hour}
	registry := "123456789012.dkr.ecr.us-east-1.amazonaws.com"

	first, err := reconciler.authorization(context.Background(), registry)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reconciler.authorization(context.Background(), registry)
	if err != nil {
		t.Fatal(err)
	}
	if ecrClient.callCount() != 1 {
		t.Fatalf("GetAuthorizationToken calls = %d, want 1", ecrClient.callCount())
	}
	if first.AuthorizationToken == nil || second.AuthorizationToken == nil || *first.AuthorizationToken != *second.AuthorizationToken {
		t.Fatal("expected reconciler to reuse the in-memory authorization token")
	}

	var waitGroup sync.WaitGroup
	for range 8 {
		waitGroup.Go(func() {
			if _, err := reconciler.authorization(context.Background(), registry); err != nil {
				t.Errorf("authorization() error = %v", err)
			}
		})
	}
	waitGroup.Wait()
	if ecrClient.callCount() != 1 {
		t.Fatalf("concurrent GetAuthorizationToken calls = %d, want 1", ecrClient.callCount())
	}

	reconciler.authorizationFetchedAt = time.Now().Add(-2 * time.Hour)
	if _, err := reconciler.authorization(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	if ecrClient.callCount() != 2 {
		t.Fatalf("GetAuthorizationToken calls after expiry = %d, want 2", ecrClient.callCount())
	}
}

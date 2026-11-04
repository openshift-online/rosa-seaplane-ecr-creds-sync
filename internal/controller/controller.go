package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/openshift/hypershift/api/hypershift/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type tokenClient interface {
	GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
}

type Reconciler struct {
	client.Client
	ECR          tokenClient
	Repository   string
	RefreshAfter time.Duration
	// SourceSecrets lists Secret names in the HostedCluster namespace that the
	// controller reads and merges into the pull-secret. Use when ESO writes the
	// base pull secret to a different name so that ESO and this controller do
	// not overwrite each other.
	SourceSecrets []string

	authorizationMu        sync.Mutex
	authorizationData      types.AuthorizationData
	authorizationFetchedAt time.Time
}

func (r *Reconciler) SetupWithManager(mgr manager.Manager, options controller.Options) error {
	return builder.ControllerManagedBy(mgr).
		WithOptions(options).
		For(&v1beta1.HostedCluster{}).
		// Re-reconcile when an owned Secret changes so that ESO overwrites are
		// detected and the ECR token is re-merged immediately.
		Owns(&corev1.Secret{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("hostedCluster", req.NamespacedName)
	var hc v1beta1.HostedCluster
	if err := r.Get(ctx, req.NamespacedName, &hc); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	if !hc.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	secretName := hc.Spec.PullSecret.Name
	if secretName == "" {
		return reconcile.Result{}, fmt.Errorf("HostedCluster has no spec.pullSecret.name")
	}

	registry, err := registryFromRepository(r.Repository)
	if err != nil {
		return reconcile.Result{}, err
	}
	auth, err := r.authorization(ctx, registry)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("get ECR authorization token: %w", err)
	}

	// Collect auths from source Secrets (e.g. base-pull-secret written by ESO)
	// and merge them with the ECR credentials so that the final pull-secret
	// contains credentials for all registries.
	merged := mergedAuths{}
	for _, sourceName := range r.SourceSecrets {
		src := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: hc.Namespace, Name: sourceName}, src); err != nil {
			if !apierrors.IsNotFound(err) {
				return reconcile.Result{}, fmt.Errorf("get source secret %q: %w", sourceName, err)
			}
			logger.Info("source secret not found, skipping — will retry on next reconcile", "secret", sourceName)
			continue
		}
		data := src.Data[corev1.DockerConfigJsonKey]
		if len(data) == 0 {
			logger.Info("source secret has no .dockerconfigjson key, skipping", "secret", sourceName)
			continue
		}
		if err := merged.merge(data); err != nil {
			logger.Error(err, "source secret has malformed dockerconfigjson, skipping", "secret", sourceName)
			continue
		}
		logger.Info("merged credentials from source secret", "secret", sourceName)
	}
	merged.addECR(registry, auth)

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: hc.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Type = corev1.SecretTypeDockerConfigJson
		secret.Data = map[string][]byte{corev1.DockerConfigJsonKey: merged.bytes()}
		return controllerutil.SetControllerReference(&hc, secret, r.Scheme())
	})
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("reconcile pull secret: %w", err)
	}

	logger.Info("reconciled HostedCluster pull secret", "secret", secretName, "sources", len(r.SourceSecrets))
	return reconcile.Result{RequeueAfter: r.RefreshAfter}, nil
}

func (r *Reconciler) authorization(ctx context.Context, registry string) (types.AuthorizationData, error) {
	r.authorizationMu.Lock()
	defer r.authorizationMu.Unlock()

	now := time.Now()
	if !r.authorizationFetchedAt.IsZero() && now.Before(r.authorizationFetchedAt.Add(r.RefreshAfter)) {
		return r.authorizationData, nil
	}

	auth, err := r.fetchAuthorization(ctx, registry)
	if err != nil {
		return types.AuthorizationData{}, err
	}
	r.authorizationData = auth
	r.authorizationFetchedAt = now
	return auth, nil
}

func (r *Reconciler) fetchAuthorization(ctx context.Context, registry string) (types.AuthorizationData, error) {
	input := &ecr.GetAuthorizationTokenInput{}
	if account := accountFromRegistry(registry); account != "" {
		input.RegistryIds = []string{account}
	}
	output, err := r.ECR.GetAuthorizationToken(ctx, input)
	if err != nil {
		return types.AuthorizationData{}, err
	}
	if len(output.AuthorizationData) == 0 || output.AuthorizationData[0].AuthorizationToken == nil {
		return types.AuthorizationData{}, fmt.Errorf("ECR returned no authorization token")
	}
	return output.AuthorizationData[0], nil
}

func registryFromRepository(repository string) (string, error) {
	value := repository
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" {
		return "", fmt.Errorf("invalid ECR repository %q", repository)
	}
	return parsed.Host, nil
}

var accountPattern = regexp.MustCompile(`^([0-9]{12})\.dkr\.ecr\.`)

func accountFromRegistry(registry string) string {
	match := accountPattern.FindStringSubmatch(registry)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

// mergedAuths accumulates docker config auths from multiple sources.
type mergedAuths struct {
	auths map[string]json.RawMessage
}

// merge parses a dockerconfigjson blob and adds its auths entries.
func (m *mergedAuths) merge(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var cfg struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse dockerconfigjson: %w", err)
	}
	if m.auths == nil {
		m.auths = make(map[string]json.RawMessage)
	}
	for k, v := range cfg.Auths {
		m.auths[k] = v
	}
	return nil
}

// addECR adds or overwrites the ECR registry entry with fresh credentials.
// auth.AuthorizationToken is already a base64-encoded "user:password" string
// — the correct value for the dockerconfig auth field.
func (m *mergedAuths) addECR(registry string, auth types.AuthorizationData) {
	if m.auths == nil {
		m.auths = make(map[string]json.RawMessage)
	}
	entry, _ := json.Marshal(map[string]string{"auth": aws.ToString(auth.AuthorizationToken)})
	m.auths[registry] = entry
}

// bytes serializes the merged auths as a dockerconfigjson blob.
func (m *mergedAuths) bytes() []byte {
	data, _ := json.Marshal(map[string]any{"auths": m.auths})
	return data
}

var _ client.Object = (*v1beta1.HostedCluster)(nil)

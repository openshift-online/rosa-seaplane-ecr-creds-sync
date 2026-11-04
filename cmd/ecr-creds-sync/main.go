package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/openshift-online/ecr-creds-sync/internal/controller"
	"github.com/openshift/hypershift/api/hypershift/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const defaultRefreshAfter = 2 * time.Hour

func refreshAfterFromEnv(value string) (time.Duration, error) {
	if value == "" {
		return defaultRefreshAfter, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse REFRESH_AFTER: %w", err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("REFRESH_AFTER must be greater than zero")
	}
	return duration, nil
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: false})))

	var repository, region, endpoint, sourceSecretsRaw string
	refreshAfter, err := refreshAfterFromEnv(os.Getenv("REFRESH_AFTER"))
	if err != nil {
		ctrl.Log.Error(err, "invalid refresh interval")
		os.Exit(2)
	}
	flag.StringVar(&repository, "ecr-repository", os.Getenv("ECR_REPOSITORY"), "ECR repository URI, for example 123456789012.dkr.ecr.us-east-1.amazonaws.com/rosa/release")
	flag.StringVar(&region, "aws-region", os.Getenv("AWS_REGION"), "AWS region; when empty, use the AWS SDK region configuration")
	flag.StringVar(&endpoint, "aws-endpoint-url", os.Getenv("AWS_ENDPOINT_URL"), "optional AWS endpoint override for local emulators such as LocalStack")
	flag.StringVar(&sourceSecretsRaw, "source-secrets", os.Getenv("SOURCE_SECRETS"), "comma-separated list of Secret names in each HCP namespace to merge into the pull-secret (e.g. base-pull-secret)")
	flag.DurationVar(&refreshAfter, "refresh-after", refreshAfter, "time between ECR authorization token refreshes")
	flag.Parse()

	if refreshAfter <= 0 {
		ctrl.Log.Error(nil, "refresh interval must be greater than zero")
		os.Exit(2)
	}
	if repository == "" {
		ctrl.Log.Error(nil, "ecr-repository is required")
		os.Exit(2)
	}

	ctx := context.Background()
	loadOptions := []func(*config.LoadOptions) error{}
	if region != "" {
		loadOptions = append(loadOptions, config.WithRegion(region))
	}
	if endpoint != "" {
		loadOptions = append(loadOptions, config.WithBaseEndpoint(endpoint))
	}
	awsConfig, err := config.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		ctrl.Log.Error(err, "unable to load AWS configuration")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1beta1.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme, HealthProbeBindAddress: ":8081"})
	if err != nil {
		ctrl.Log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	var sourceSecrets []string
	for _, s := range strings.Split(sourceSecretsRaw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			sourceSecrets = append(sourceSecrets, s)
		}
	}

	r := &controller.Reconciler{
		Client:        mgr.GetClient(),
		ECR:           ecr.NewFromConfig(awsConfig),
		Repository:    repository,
		RefreshAfter:  refreshAfter,
		SourceSecrets: sourceSecrets,
	}
	if err := r.SetupWithManager(mgr, crcontroller.Options{MaxConcurrentReconciles: 4}); err != nil {
		ctrl.Log.Error(err, "unable to create controller")
		os.Exit(1)
	}
	_ = mgr.AddHealthzCheck("health", healthz.Ping)
	_ = mgr.AddReadyzCheck("ready", healthz.Ping)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "manager stopped")
		os.Exit(1)
	}
}

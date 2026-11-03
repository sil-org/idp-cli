package loginlog

import (
	"os"
	"path/filepath"
	"testing"
)

// useFakeAWSFiles points the AWS SDK at throwaway shared config/credentials
// files and clears env vars that would otherwise take precedence.
func useFakeAWSFiles(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	configFile := filepath.Join(dir, "config")
	credsFile := filepath.Join(dir, "credentials")
	writeFile(t, configFile, "[default]\nregion = us-east-1\n\n[profile audit]\nregion = eu-west-2\n")
	writeFile(t, credsFile, "[default]\naws_access_key_id = DEFAULTKEY\naws_secret_access_key = x\n\n"+
		"[audit]\naws_access_key_id = AUDITKEY\naws_secret_access_key = y\n")

	t.Setenv("AWS_CONFIG_FILE", configFile)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credsFile)
	for _, v := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNewClient_ProfileSelectsRegionAndCredentials(t *testing.T) {
	useFakeAWSFiles(t)

	client, err := NewClient(t.Context(), ClientOptions{Profile: "audit"})
	if err != nil {
		t.Fatal(err)
	}
	opts := client.Options()
	if opts.Region != "eu-west-2" {
		t.Errorf("Region = %q, want the profile's eu-west-2", opts.Region)
	}
	creds, err := opts.Credentials.Retrieve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "AUDITKEY" {
		t.Errorf("AccessKeyID = %q, want the profile's AUDITKEY", creds.AccessKeyID)
	}
}

func TestNewClient_NoProfileUsesDefault(t *testing.T) {
	useFakeAWSFiles(t)

	client, err := NewClient(t.Context(), ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := client.Options().Credentials.Retrieve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "DEFAULTKEY" {
		t.Errorf("AccessKeyID = %q, want DEFAULTKEY", creds.AccessKeyID)
	}
}

func TestNewClient_RegionOverridesProfileRegion(t *testing.T) {
	useFakeAWSFiles(t)

	client, err := NewClient(t.Context(), ClientOptions{Profile: "audit", Region: "ap-southeast-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.Options().Region; got != "ap-southeast-1" {
		t.Errorf("Region = %q, want the explicit ap-southeast-1", got)
	}
}

func TestNewClient_UnknownProfileFails(t *testing.T) {
	useFakeAWSFiles(t)

	if _, err := NewClient(t.Context(), ClientOptions{Profile: "nope"}); err == nil {
		t.Error("expected an error for a profile that doesn't exist")
	}
}

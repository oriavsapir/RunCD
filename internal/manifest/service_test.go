package manifest

import (
	"strconv"
	"testing"
)

const validDigest = "sha256:3f8a1c0000000000000000000000000000000000000000000000000000000000"

func TestParse_DigestOnlyIsValid(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
`)
	sd, err := Parse(yaml)
	if err != nil {
		t.Fatalf("expected valid, got error: %v", err)
	}
	if sd.Image.Digest != validDigest {
		t.Fatalf("digest not parsed: %+v", sd.Image)
	}
	if sd.ResourceType != ResourceService {
		t.Fatalf("expected default resourceType=service, got %q", sd.ResourceType)
	}
}

func TestParse_DigestWithTrackIsValid(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
  track: main
  repository: us-central1-docker.pkg.dev/proj/repo/image
`)
	if _, err := Parse(yaml); err != nil {
		t.Fatalf("expected valid, got error: %v", err)
	}
}

func TestParse_DigestWithVersionIsValid(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
  version: v1.4.2
  repository: us-central1-docker.pkg.dev/proj/repo/image
`)
	if _, err := Parse(yaml); err != nil {
		t.Fatalf("expected valid, got error: %v", err)
	}
}

func TestParse_TrackAndVersionTogetherRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
  track: main
  version: v1.4.2
  repository: us-central1-docker.pkg.dev/proj/repo/image
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error when both track and version are set")
	}
}

func TestParse_TrackWithoutRepositoryRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
  track: main
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error when track is set without repository")
	}
}

func TestParse_VersionWithoutRepositoryRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
  version: v1.4.2
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error when version is set without repository")
	}
}

func TestParse_MissingDigestRejected(t *testing.T) {
	yaml := []byte(`
image:
  track: main
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error when image.digest is missing")
	}
}

func TestParse_BareTagRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: latest
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a bare/floating tag in image.digest")
	}
}

func TestParse_MutableTagLikeDigestRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: myimage:v1.2.3
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a tag-shaped value in image.digest")
	}
}

func TestParse_MalformedDigestRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: sha256:not-hex
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a malformed digest")
	}
}

func TestParse_InvalidResourceTypeRejected(t *testing.T) {
	yaml := []byte(`
resourceType: pod
image:
  digest: ` + validDigest + `
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an unknown resourceType")
	}
}

func TestParse_ExplicitResourceTypes(t *testing.T) {
	for _, rt := range []string{"service", "job", "workerPool"} {
		yaml := []byte(`
resourceType: ` + rt + `
image:
  digest: ` + validDigest + `
`)
		sd, err := Parse(yaml)
		if err != nil {
			t.Fatalf("resourceType=%s: unexpected error: %v", rt, err)
		}
		if string(sd.ResourceType) != rt {
			t.Fatalf("resourceType=%s: got %q", rt, sd.ResourceType)
		}
	}
}

// TestParse_TrafficPercentOutOfRangeRejected covers both out-of-[0,100]
// values and, crucially, in-range-but-unsupported ones (e.g. 50) — the real
// deploy path (cloudrun.validatedPercent) only ever accepts exactly 100.
func TestParse_TrafficPercentOutOfRangeRejected(t *testing.T) {
	for _, percent := range []int{-1, 0, 1, 50, 99, 101, 500} {
		yaml := []byte(`
image:
  digest: ` + validDigest + `
traffic:
  latestRevisionPercent: ` + strconv.Itoa(percent) + `
`)
		if _, err := Parse(yaml); err == nil {
			t.Fatalf("percent=%d: expected error for an out-of-range traffic percent", percent)
		}
	}
}

func TestParse_TrafficPercentInRangeIsValid(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
traffic:
  latestRevisionPercent: 100
`)
	sd, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sd.Traffic == nil || sd.Traffic.LatestRevisionPercent == nil || *sd.Traffic.LatestRevisionPercent != 100 {
		t.Fatalf("traffic not parsed: %+v", sd.Traffic)
	}
}

func TestParse_EnvAndSecrets(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
env:
  LOG_LEVEL: debug
secrets:
  - name: DB_PASSWORD
    secret: db-password
    version: "3"
  - name: API_KEY
    secret: api-key
`)
	sd, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sd.Env["LOG_LEVEL"] != "debug" {
		t.Fatalf("env not parsed: %+v", sd.Env)
	}
	if len(sd.Secrets) != 2 || sd.Secrets[0].Name != "DB_PASSWORD" || sd.Secrets[0].Version != "3" {
		t.Fatalf("secrets not parsed: %+v", sd.Secrets)
	}
}

func TestParse_SecretMissingNameRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
secrets:
  - secret: db-password
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a secret entry missing name")
	}
}

func TestParse_SecretMissingSecretRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
secrets:
  - name: DB_PASSWORD
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a secret entry missing secret")
	}
}

func TestParse_DuplicateSecretNameRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
secrets:
  - name: DB_PASSWORD
    secret: db-password-a
  - name: DB_PASSWORD
    secret: db-password-b
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a secret name listed twice")
	}
}

func TestParse_EnvVarNameInBothEnvAndSecretsRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
env:
  DB_PASSWORD: literal-value
secrets:
  - name: DB_PASSWORD
    secret: db-password
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for a var name declared in both env and secrets")
	}
}

func TestParse_EmptyEnvVarNameRejected(t *testing.T) {
	yaml := []byte(`
image:
  digest: ` + validDigest + `
env:
  "": literal-value
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error for an env entry with an empty name")
	}
}

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
`)
	if _, err := Parse(yaml); err == nil {
		t.Fatal("expected error when both track and version are set")
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

func TestParse_TrafficPercentOutOfRangeRejected(t *testing.T) {
	for _, percent := range []int{-1, 101, 500} {
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

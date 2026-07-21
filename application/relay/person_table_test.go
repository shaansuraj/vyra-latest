package relay

import "testing"

func TestEnrollmentMinProfilesDefaultsToOne(t *testing.T) {
	t.Setenv("FACE_ENROLLMENT_MIN_PROFILES", "")

	if got := enrollmentMinProfiles(); got != 1 {
		t.Fatalf("expected default minimum profiles to be 1, got %d", got)
	}

	p := &Person{HasFace: true, ProfileCount: 1}
	applyEnrollmentStatus(p)
	if !p.EnrollmentReady {
		t.Fatalf("expected one accepted profile to be ready, reason=%q", p.EnrollmentStatusReason)
	}
}

func TestEnrollmentMinProfilesCanBeRaised(t *testing.T) {
	t.Setenv("FACE_ENROLLMENT_MIN_PROFILES", "3")

	p := &Person{HasFace: true, ProfileCount: 1}
	applyEnrollmentStatus(p)
	if p.EnrollmentReady {
		t.Fatalf("expected one profile not to be ready when minimum is raised to 3")
	}
}

package autoupdate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Version struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease string
	Build      string
	Raw        string
}

// ValidateVersion checks that s is a non-empty SemVer 2.0 string (with an
// optional leading "v"). It is the entry point used by the admin API.
func ValidateVersion(s string) error {
	if _, err := ParseVersion(s); err != nil {
		return err
	}
	return nil
}

func ParseVersion(s string) (*Version, error) {
	if s == "" {
		return nil, errors.New("empty version string")
	}

	v := &Version{Raw: s}
	core := s
	if strings.HasPrefix(core, "v") || strings.HasPrefix(core, "V") {
		core = core[1:]
	}

	if idx := strings.Index(core, "+"); idx >= 0 {
		v.Build = core[idx+1:]
		core = core[:idx]
	}

	if idx := strings.Index(core, "-"); idx >= 0 {
		v.Prerelease = core[idx+1:]
		core = core[:idx]
	}

	parts := strings.Split(core, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return nil, fmt.Errorf("invalid semver format: %s", s)
	}

	ints := make([]int, 3)
	for i := 0; i < 3; i++ {
		if i < len(parts) {
			n, err := strconv.Atoi(parts[i])
			if err != nil || n < 0 {
				return nil, fmt.Errorf("invalid semver segment: %s", parts[i])
			}
			ints[i] = n
		}
	}

	v.Major = ints[0]
	v.Minor = ints[1]
	v.Patch = ints[2]
	return v, nil
}

func (v *Version) Compare(other *Version) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	if v.Prerelease == "" && other.Prerelease != "" {
		return 1
	}
	if v.Prerelease != "" && other.Prerelease == "" {
		return -1
	}
	if v.Prerelease != other.Prerelease {
		if v.Prerelease < other.Prerelease {
			return -1
		}
		return 1
	}
	return 0
}

func (v *Version) String() string {
	out := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Prerelease != "" {
		out += "-" + v.Prerelease
	}
	if v.Build != "" {
		out += "+" + v.Build
	}
	return out
}

func (v *Version) IsNewerThan(other *Version) bool {
	return v.Compare(other) > 0
}

func (v *Version) IsOlderThan(other *Version) bool {
	return v.Compare(other) < 0
}

func (v *Version) IsCompatibleWith(other *Version) bool {
	return v.Major == other.Major
}

func IsCompatible(current, target string) (bool, error) {
	cv, err := ParseVersion(current)
	if err != nil {
		return false, err
	}
	tv, err := ParseVersion(target)
	if err != nil {
		return false, err
	}
	return cv.IsCompatibleWith(tv), nil
}

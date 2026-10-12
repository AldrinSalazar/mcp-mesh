package update

import (
	"cmp"
	"strconv"
	"strings"
)

// Version is a release version as builds report it: a tag ("v1.2.0",
// "1.3.0-rc.1"), or what git describe makes of a later commit
// ("v1.2.0-3-gabc1234", maybe with "-dirty").
type Version struct {
	Major, Minor, Patch int
	// Pre is the pre-release ("rc.1"); empty for a release.
	Pre string
	// Commits counts the commits after the tag (git describe); a build with
	// commits is newer than its tag.
	Commits int
}

// Parse reads a version. ok is false for builds that have none, such as
// "dev" or a bare commit hash.
func Parse(s string) (v Version, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s = strings.TrimSuffix(s, "-dirty")
	s, _, _ = strings.Cut(s, "+") // build metadata doesn't count
	core, pre, _ := strings.Cut(s, "-")
	// git describe appends -<commits>-g<hash>.
	if i := strings.LastIndex(pre, "-g"); i >= 0 {
		rest, hash := pre[:i], pre[i+2:]
		if isHex(hash) {
			head, count, found := cutLast(rest, "-")
			if !found {
				head, count = "", rest
			}
			if n, err := strconv.Atoi(count); err == nil && n >= 0 {
				pre, v.Commits = head, n
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	nums := [3]*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, false
		}
		*nums[i] = n
	}
	v.Pre = pre
	return v, true
}

func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

func isHex(s string) bool {
	if len(s) < 4 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// Prerelease reports whether v is a pre-release build.
func (v Version) Prerelease() bool { return v.Pre != "" }

// Compare orders versions by semver precedence; commits after a tag come
// after the tag.
func (v Version) Compare(w Version) int {
	if c := cmp.Compare(v.Major, w.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, w.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Patch, w.Patch); c != 0 {
		return c
	}
	if c := comparePre(v.Pre, w.Pre); c != 0 {
		return c
	}
	return cmp.Compare(v.Commits, w.Commits)
}

// comparePre follows semver: a release outranks its pre-releases, and
// identifiers compare numerically when both are numbers.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(as), len(bs)) {
		x, xErr := strconv.Atoi(as[i])
		y, yErr := strconv.Atoi(bs[i])
		var c int
		switch {
		case xErr == nil && yErr == nil:
			c = cmp.Compare(x, y)
		case xErr == nil:
			c = -1
		case yErr == nil:
			c = 1
		default:
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

// Behind reports whether version is older than reference. Either one
// without a version (a dev build) is never behind.
func Behind(version, reference string) bool {
	v, ok := Parse(version)
	r, rok := Parse(reference)
	return ok && rok && v.Compare(r) < 0
}

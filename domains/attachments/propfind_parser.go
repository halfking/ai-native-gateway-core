//go:build cloudreve_storage

package attachments

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// propfindResponse is the RFC 4918 § 9.2.1 multistatus envelope returned by
// a WebDAV PROPFIND. We only care about the href of each response element.
type propfindResponse struct {
	XMLName   xml.Name          `xml:"multistatus"`
	Responses []propfindHrefRow `xml:"response"`
}

type propfindHrefRow struct {
	Href     string             `xml:"href"`
	PropStat []propfindPropStat `xml:"propstat"`
}

type propfindPropStat struct {
	Prop   propfindProp `xml:"prop"`
	Status string       `xml:"status"`
}

type propfindProp struct {
	ResourceType propfindType `xml:"resourcetype"`
	// Most Cloudreve/sabre-dav setups include a <getcontentlength> entry; we
	// ignore it because Cloudreve StorageBackend.GetMetadata derives size
	// from HEAD, not PROPFIND — keeping the parser narrower is healthier.
}

type propfindType struct {
	Collection string `xml:"collection"`
}

// parsePropfindResponse extracts every href that is NOT the queried target.
//
//	targetURL is the absolute URL passed to PROPFIND and is filtered out so
//	callers receive only children, not the queried resource itself.
//
// hrefs may be returned as either absolute URLs (some Cloudreve configs) or
// root-relative paths (sabredav default). Both shapes are tolerated.
func parsePropfindResponse(body string, targetURL string) ([]string, error) {
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	var parsed propfindResponse
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("xml unmarshal: %w", err)
	}

	// Normalize the target so equality comparisons tolerate a trailing slash
	// or a missing one.
	target := strings.TrimRight(targetURL, "/")

	// Path-only form for matching hrefs that arrived as root-relative paths.
	targetPath := ""
	if idx := strings.Index(target, "://"); idx >= 0 {
		// Look for the first slash after the host.
		if slashIdx := strings.Index(target[idx+3:], "/"); slashIdx >= 0 {
			targetPath = target[idx+3+slashIdx:]
		}
	} else if strings.HasPrefix(target, "/") {
		targetPath = target
	}

	hits := make([]string, 0, len(parsed.Responses))
	for _, r := range parsed.Responses {
		href := strings.TrimSpace(r.Href)
		if href == "" {
			continue
		}

		// Filter out the queried target itself.
		equalTarget := href == target || href == target+"/"
		equalTargetPath := targetPath != "" && (href == targetPath || href == targetPath+"/")
		if equalTarget || equalTargetPath {
			continue
		}

		if strings.HasSuffix(href, "/") {
			// A trailing slash = collection. Skip directories; the canonical
			// interface deals in file keys.
			continue
		}
		hits = append(hits, href)
	}
	return hits, nil
}

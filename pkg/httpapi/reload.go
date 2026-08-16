package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/open-nv/clisynth/pkg/outputfsm"
)

const (
	DefaultMaxReloadBodyBytes    int64 = 32 << 20
	AbsoluteMaxReloadBodyBytes   int64 = 256 << 20
	DefaultMaxReloadDefinitions        = 10_000
	AbsoluteMaxReloadDefinitions       = 100_000

	reloadAPIVersion = "clisynth.opennv.io/v1alpha1"
	reloadKind       = "OutputFSMBundle"
)

// ReloadBundle is a complete, immutable GitOps snapshot. Count, digest, and
// complete are mandatory so a truncated request cannot be mistaken for a
// deliberate delta update.
type ReloadBundle struct {
	APIVersion    string           `json:"apiVersion"`
	Kind          string           `json:"kind"`
	Revision      string           `json:"revision"`
	Complete      bool             `json:"complete"`
	ExpectedCount int              `json:"expectedCount"`
	Digest        string           `json:"digest"`
	Definitions   []ReloadDocument `json:"definitions"`
}

// ReloadDocument carries one unmodified OutputFSM YAML document and its
// repository-relative source path.
type ReloadDocument struct {
	Source string `json:"source"`
	YAML   string `json:"yaml"`
}

type ReloadResponse struct {
	APIVersion string `json:"apiVersion"`
	Revision   string `json:"revision"`
	Count      int    `json:"count"`
	Digest     string `json:"digest"`
}

func validateReloadToken(token string) error {
	if len(token) < 32 || len(token) > 4096 {
		return errors.New("reload token must contain between 32 and 4096 bytes")
	}
	for _, value := range []byte(token) {
		if value < 0x21 || value > 0x7e {
			return errors.New("reload token must contain visible ASCII without whitespace")
		}
	}
	return nil
}

func hashReloadToken(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

func (a *API) reloadDefinitions(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !a.authorizeReload(request) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="clisynth-reload"`)
		writeAPIError(writer, http.StatusUnauthorized, "unauthorized", "valid reload bearer token required")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}

	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	var bundle ReloadBundle
	if err := a.decodeReloadJSON(writer, request, &bundle); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(writer, http.StatusRequestEntityTooLarge, "bundle_too_large", fmt.Sprintf("reload body exceeds %d bytes", a.maxReloadBytes))
			return
		}
		writeAPIError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	definitions, digest, err := a.validateReloadBundle(bundle)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_bundle", err.Error())
		return
	}
	if err := a.engine.Registry().Replace(definitions); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_bundle", err.Error())
		return
	}
	a.logger.Info("definition registry reloaded", "revision", bundle.Revision, "definitions", len(definitions), "digest", digest)
	writeJSON(writer, http.StatusOK, ReloadResponse{
		APIVersion: reloadAPIVersion,
		Revision:   bundle.Revision,
		Count:      len(definitions),
		Digest:     digest,
	})
}

func (a *API) authorizeReload(request *http.Request) bool {
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	header := values[0]
	separator := strings.IndexByte(header, ' ')
	if separator < 1 || !strings.EqualFold(header[:separator], "Bearer") {
		return false
	}
	token := header[separator+1:]
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t") {
		return false
	}
	candidate := hashReloadToken(token)
	return subtle.ConstantTimeCompare(candidate[:], a.reloadTokenHash[:]) == 1
}

func (a *API) decodeReloadJSON(writer http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, a.maxReloadBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("request body must contain exactly one JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func (a *API) validateReloadBundle(bundle ReloadBundle) ([]outputfsm.Definition, string, error) {
	if bundle.APIVersion != reloadAPIVersion {
		return nil, "", fmt.Errorf("apiVersion must be %q", reloadAPIVersion)
	}
	if bundle.Kind != reloadKind {
		return nil, "", fmt.Errorf("kind must be %q", reloadKind)
	}
	if !bundle.Complete {
		return nil, "", errors.New("complete must be true; incremental reloads are not accepted")
	}
	if err := validateRevision(bundle.Revision); err != nil {
		return nil, "", err
	}
	if bundle.ExpectedCount < 1 || bundle.ExpectedCount > a.maxReloadDefs {
		return nil, "", fmt.Errorf("expectedCount must be between 1 and %d", a.maxReloadDefs)
	}
	if len(bundle.Definitions) != bundle.ExpectedCount {
		return nil, "", fmt.Errorf("expectedCount is %d but bundle contains %d definitions", bundle.ExpectedCount, len(bundle.Definitions))
	}
	if len(bundle.Definitions) > a.maxReloadDefs {
		return nil, "", fmt.Errorf("bundle contains %d definitions; maximum is %d", len(bundle.Definitions), a.maxReloadDefs)
	}
	if err := validateDigest(bundle.Digest); err != nil {
		return nil, "", err
	}

	sources := make(map[string]struct{}, len(bundle.Definitions))
	for index, document := range bundle.Definitions {
		if err := validateDocumentEnvelope(document); err != nil {
			return nil, "", fmt.Errorf("definition %d: %w", index, err)
		}
		if _, duplicate := sources[document.Source]; duplicate {
			return nil, "", fmt.Errorf("definition %d duplicates source %q", index, document.Source)
		}
		sources[document.Source] = struct{}{}
	}

	digest := ComputeReloadDigest(bundle.Definitions)
	if subtle.ConstantTimeCompare([]byte(bundle.Digest), []byte(digest)) != 1 {
		return nil, "", fmt.Errorf("digest mismatch: computed %s", digest)
	}

	definitions := make([]outputfsm.Definition, 0, len(bundle.Definitions))
	for index, document := range bundle.Definitions {
		definition, err := outputfsm.Decode([]byte(document.YAML))
		if err != nil {
			return nil, "", fmt.Errorf("definition %d (%s): %w", index, document.Source, err)
		}
		definition.Source = document.Source
		definitions = append(definitions, definition)
	}
	return definitions, digest, nil
}

func validateRevision(revision string) error {
	if revision == "" || len(revision) > 256 {
		return errors.New("revision must contain between 1 and 256 visible ASCII bytes")
	}
	for _, value := range []byte(revision) {
		if value < 0x21 || value > 0x7e {
			return errors.New("revision must contain between 1 and 256 visible ASCII bytes")
		}
	}
	return nil
}

func validateDigest(digest string) error {
	if len(digest) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(digest, "sha256:") {
		return errors.New("digest must use sha256:<64 lowercase hexadecimal characters>")
	}
	encoded := strings.TrimPrefix(digest, "sha256:")
	decoded, err := hex.DecodeString(encoded)
	if err != nil || hex.EncodeToString(decoded) != encoded {
		return errors.New("digest must use sha256:<64 lowercase hexadecimal characters>")
	}
	return nil
}

func validateDocumentEnvelope(document ReloadDocument) error {
	if document.Source == "" || len(document.Source) > 512 || !utf8.ValidString(document.Source) {
		return errors.New("source must contain between 1 and 512 UTF-8 bytes")
	}
	if strings.Contains(document.Source, "\\") || strings.HasPrefix(document.Source, "/") || path.Clean(document.Source) != document.Source || document.Source == "." || strings.HasPrefix(document.Source, "../") {
		return fmt.Errorf("source %q must be a clean repository-relative POSIX path", document.Source)
	}
	extension := strings.ToLower(path.Ext(document.Source))
	if extension != ".yaml" && extension != ".yml" {
		return fmt.Errorf("source %q must end in .yaml or .yml", document.Source)
	}
	if document.YAML == "" {
		return errors.New("yaml must not be empty")
	}
	if len(document.YAML) > outputfsm.MaxDefinitionBytes {
		return fmt.Errorf("yaml exceeds %d bytes", outputfsm.MaxDefinitionBytes)
	}
	if !utf8.ValidString(document.YAML) {
		return errors.New("yaml must be valid UTF-8")
	}
	return nil
}

// ComputeReloadDigest returns the transport digest used by ReloadBundle. It is
// SHA-256 over documents sorted by source, with each UTF-8 source and YAML byte
// string prefixed by its unsigned 64-bit big-endian length.
func ComputeReloadDigest(documents []ReloadDocument) string {
	sorted := append([]ReloadDocument(nil), documents...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Source < sorted[j].Source })
	hash := sha256.New()
	var size [8]byte
	for _, document := range sorted {
		binary.BigEndian.PutUint64(size[:], uint64(len(document.Source)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(document.Source))
		binary.BigEndian.PutUint64(size[:], uint64(len(document.YAML)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(document.YAML))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

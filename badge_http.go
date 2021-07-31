// Package badge provides repo badge related functions.
package badge

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/bradleyfalzon/ghinstallation"
	"github.com/google/go-github/v37/github"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
)

const (
	envPrivateKeySecret = "AB_PRIVATE_KEY_SECRET_NAME"
	envGHAppID          = "AB_GH_APP_ID"
)

var appsTransport *ghinstallation.AppsTransport

func init() {
	privateKey := githubPrivateKey()
	appID, err := strconv.ParseInt(os.Getenv(envGHAppID), 10, 64)
	if err != nil {
		panic(err)
	}
	appsTransport = newGitHubTransport(appID, privateKey)
}

// GenBadgeHTTP is a HTTP cloud function that returns a badge.
func GenBadgeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Decode params.
	repoParam := r.FormValue("repo")
	if repoParam == "" {
		http.Error(w, "Missing repo key", http.StatusBadRequest)
		return
	}
	repoParts := strings.SplitN(repoParam, "/", 2)
	if len(repoParts) != 2 {
		http.Error(w, "Invalid repo key", http.StatusBadRequest)
		return
	}
	owner := repoParts[0]
	repo := repoParts[1]
	branch := r.FormValue("branch")
	if branch == "" {
		http.Error(w, "Missing branch key", http.StatusBadRequest)
		return
	}
	runName := r.FormValue("run")
	if runName == "" {
		http.Error(w, "Missing run key", http.StatusBadRequest)
		return
	}
	badgeName := r.FormValue("badge")
	if badgeName == "" {
		http.Error(w, "Missing badge key", http.StatusBadRequest)
		return
	}
	subject := r.FormValue("subject")
	if subject == "" {
		http.Error(w, "Missing subject key", http.StatusBadRequest)
		return
	}
	// Get installation ID.
	appClient := github.NewClient(&http.Client{Transport: appsTransport})
	installation, _, err := appClient.Apps.FindRepositoryInstallation(ctx, owner, repo)
	if err != nil || installation == nil {
		http.Error(w, "Can't find installation for repo", http.StatusBadRequest)
		return
	}
	// Create repo client.
	repoTransport := ghinstallation.NewFromAppsTransport(appsTransport, installation.GetID())
	repoClient := github.NewClient(&http.Client{Transport: repoTransport})
	// List runs in repo.
	runs, _, err := repoClient.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, &github.ListWorkflowRunsOptions{
		Branch: branch,
		Event:  "push",
		Status: "success",
	})
	if err != nil {
		http.Error(w, "Failed to list runs", http.StatusBadRequest)
		return
	}
	// Find run matching run name.
	var runID int64
	for _, run := range runs.WorkflowRuns {
		if strings.ToLower(run.GetName()) == strings.ToLower(runName) {
			runID = run.GetID()
			break
		}
	}
	if runID == 0 {
		http.Error(w, "No run found", http.StatusBadRequest)
		return
	}
	// Get artifacts.
	artifacts, _, err := repoClient.Actions.ListWorkflowRunArtifacts(ctx, owner, repo, runID, &github.ListOptions{})
	if err != nil {
		http.Error(w, "Failed to get artifacts", http.StatusBadRequest)
		return
	}
	// Find artifact matching name.
	var downloadURL string
	for _, artifact := range artifacts.Artifacts {
		if artifact.GetName() == "badge_"+badgeName {
			downloadURL = artifact.GetArchiveDownloadURL()
			break
		}
	}
	if downloadURL == "" {
		http.Error(w, "Artifact not found in "+strconv.FormatInt(runID, 10), http.StatusBadRequest)
		return
	}
	status, err := loadArtifact(ctx, repoClient.Client(), downloadURL)
	if err != nil {
		http.Error(w, "Failed to download artifact: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Create badge.
	badge := Badge{
		Subject: subject,
		Status:  status,
		Color:   r.FormValue("color"),
		Label:   r.FormValue("label"),
		List:    r.FormValue("list"),
		Icon:    r.FormValue("icon"),
	}
	// Redirect to badge URL.
	http.Redirect(w, r, badge.URL(), http.StatusSeeOther)
}

// Badge is a GitHub Badge.
type Badge struct {
	Subject string
	Status  string
	Color   string
	Label   string
	List    string
	Icon    string
}

// URL returns the link pointing to the badge image.
// Service provided by https://badgen.net/
func (b *Badge) URL() string {
	values := make(url.Values)
	if b.Color != "" {
		values.Set("color", b.Color)
	}
	if b.Label != "" {
		values.Set("label", b.Label)
	}
	if b.List != "" {
		values.Set("list", b.List)
	}
	if b.Icon != "" {
		values.Set("icon", b.Icon)
	}
	return fmt.Sprintf("https://badgen.net/badge/%s/%s?%s",
		url.PathEscape(b.Subject),
		url.PathEscape(b.Status),
		values.Encode())
}

func githubPrivateKey() []byte {
	ctx := context.Background()
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		log.Fatalf("Failed to create secret manager client: %s", err)
	}
	request := &secretmanagerpb.AccessSecretVersionRequest{
		Name: os.Getenv(envPrivateKeySecret),
	}
	secret, err := client.AccessSecretVersion(ctx, request)
	if err != nil {
		log.Fatalf("Failed to retrieve GitHub private key: %s", err)
	}
	return secret.GetPayload().GetData()
}

func newGitHubTransport(appID int64, privateKey []byte) *ghinstallation.AppsTransport {
	tr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appID, privateKey)
	if err != nil {
		log.Fatalf("Failed to create OAuth transport: %s", err)
	}
	return tr
}

func loadArtifact(ctx context.Context, client *http.Client, downloadURL string) (string, error) {
	// Submit download request.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	//req.Header.Set("accept", "application/zip")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %s", res.Status)
	}
	// Read body (1K max).
	zipBuf, err := ioutil.ReadAll(io.LimitReader(res.Body, 1024))
	if err != nil {
		return "", err
	}
	// Read ZIP header.
	rd, err := zip.NewReader(bytes.NewReader(zipBuf), int64(len(zipBuf)))
	if err != nil {
		return "", err
	}
	// Find first file.
	var zipFile *zip.File
	for _, currentZipFile := range rd.File {
		if !currentZipFile.FileInfo().IsDir() {
			zipFile = currentZipFile
		}
	}
	if zipFile == nil {
		return "null", nil
	}
	// Open file in ZIP.
	stream, err := zipFile.Open()
	if err != nil {
		return "", err
	}
	// Extract first line.
	bodyBuf, err := ioutil.ReadAll(io.LimitReader(stream, 128))
	if err != nil {
		return "", err
	}
	lines := strings.SplitN(string(bodyBuf), "\n", 2)
	if len(lines) == 0 {
		return "null", nil
	}
	firstLine := strings.TrimSpace(lines[0])
	if firstLine == "" {
		return "null", nil
	}
	return firstLine, nil
}

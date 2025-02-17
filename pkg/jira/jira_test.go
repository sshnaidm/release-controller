package jira

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/andygrunwald/go-jira"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/github/fakegithub"
	"k8s.io/test-infra/prow/jira/fakejira"
	"k8s.io/test-infra/prow/plugins"
)

type fakeGHClient struct {
	GetIssueLabelsError error
	*fakegithub.FakeClient
}

func (f fakeGHClient) GetIssueLabels(owner, repo string, number int) ([]github.Label, error) {
	if f.GetIssueLabelsError != nil {
		return nil, f.GetIssueLabelsError
	}
	return f.FakeClient.GetIssueLabels(owner, repo, number)
}

func TestGetPRS(t *testing.T) {
	issue := jira.Issue{ID: "OCPBUGS-0000"}
	removeLinkArray := []jira.RemoteLink{
		{
			ID:           1234,
			Self:         "https://issues.redhat.com/rest/api/2/issue/OCPBUGSM-0000/remotelink/0000",
			GlobalID:     "EXTBZ-14641175-Red Hat Errata Tool-0000",
			Application:  nil,
			Relationship: "external trackers",
			Object: &jira.RemoteLinkObject{
				URL:   "https://errata.devel.redhat.com/advisory/0000",
				Title: "Red Hat Errata Tool 95802",
			},
		},
		{
			ID:           1234,
			Self:         "https://issues.redhat.com/rest/api/2/issue/OCPBUGSM-0000/remotelink/1234",
			GlobalID:     "EXTBZ-14641175-Github-openshift/kube-state-metrics/pull/000",
			Application:  nil,
			Relationship: "external trackers",
			Object: &jira.RemoteLinkObject{
				URL:   "https://github.com/openshift/kube-state-metrics/pull/000",
				Title: "Red Hat Errata Tool 95802",
			},
		},
	}
	remoteLinks := make(map[string][]jira.RemoteLink)
	remoteLinks["OCPBUGS-0000"] = removeLinkArray

	c := &fakejira.FakeClient{Issues: []*jira.Issue{&issue}, RemovedLinks: removeLinkArray, ExistingLinks: remoteLinks}

	extLinks, errors := getPRs([]string{"OCPBUGS-0000"}, c)

	if len(errors) != 0 {
		t.Fatalf("unexpected errors: %s", errors)
	}

	for key, value := range extLinks {
		if key != "OCPBUGS-0000" {
			t.Fatalf("unexpected key for external links: %s", key)
		}
		if len(value) != 1 {
			t.Fatalf("unexpected number of external links: %v", extLinks)
		}
		if !reflect.DeepEqual(value[0], pr{org: "openshift", repo: "kube-state-metrics", prNum: 0}) {
			t.Fatalf("unexpected value for the external links. Expecting: %v but got: %v", pr{org: "openshift", repo: "kube-state-metrics", prNum: 0}, value[0])
		}
	}
}

func TestIssueTargetReleaseCheck(t *testing.T) {
	issueJSON := "{\n \"id\":\"0000\",\n\"key\":\"OCPBUGS-0000\",\n\"fields\":{\n \"customfield_12319940\": [\n{\n\"name\": \"4.11.Z\"\n}\n]\n}\n}"

	var issue jira.Issue
	err := json.Unmarshal([]byte(issueJSON), &issue)
	if err != nil {
		t.Fatalf("failed to unmarshall test issue")
	}
	testCases := []struct {
		name       string
		tagRelease string
		expected   bool
	}{
		{
			name:       "Valid Tag",
			tagRelease: "4.11",
			expected:   false,
		},
		{
			name:       "Invalid tag",
			tagRelease: "4.12",
			expected:   true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			check, err := issueTargetReleaseCheck(&issue, tc.tagRelease, "test")
			if err != nil {
				t.Fatalf("unexpected errors: %s", err)
			}
			if check != tc.expected {
				t.Errorf("expected  %t but got %t for tagVersion: %v", tc.expected, check, tc.tagRelease)
			}
		})
	}
}

func readJSONIntoObject(issueJSON string, issue *jira.Issue) error {
	if err := json.Unmarshal([]byte(issueJSON), &issue); err != nil {
		return fmt.Errorf("failed to unmarshall the json to a struct")
	}
	return nil
}

func TestVerifyIssues(t *testing.T) {
	type jiraFakeClientData struct {
		issues        []*jira.Issue
		remoteLinks   []jira.RemoteLink
		existingLinks map[string][]jira.RemoteLink
		transitions   []jira.Transition
	}

	type gitHubFakeClientData struct {
		issueLabelsExisting []string
	}

	type expectedResult struct {
		errors  []error
		status  string
		message string
	}

	// since the VerifyIssues command may modify issues, make a separate copy for each test
	var onQAIssue jira.Issue
	var onQAIssue2 jira.Issue
	var onQAIssue3 jira.Issue
	var verifiedIssue jira.Issue
	var inProgressIssue jira.Issue
	var inProgressIssue2 jira.Issue

	issuesToUnmarshall := []struct {
		issueJSON string
		object    *jira.Issue
	}{
		{
			issueJSON: onQAIssueJSON,
			object:    &onQAIssue,
		},
		{
			issueJSON: onQAIssueJSON,
			object:    &onQAIssue2,
		},
		{
			issueJSON: onQAIssueJSON,
			object:    &onQAIssue3,
		},
		{
			issueJSON: verifiedIssueJSON,
			object:    &verifiedIssue,
		},
		{
			issueJSON: inProgressIssueJSON,
			object:    &inProgressIssue,
		},
		{
			issueJSON: inProgressIssueJSON,
			object:    &inProgressIssue2,
		},
	}

	for _, issue := range issuesToUnmarshall {
		if err := readJSONIntoObject(issue.issueJSON, issue.object); err != nil {
			t.Fatalf(err.Error())
		}
	}

	var remoteLink []jira.RemoteLink

	err := json.Unmarshal([]byte(remoteLinksJSON), &remoteLink)
	if err != nil {
		t.Fatalf("Failed to unmarshall remoteLinksJSON")
	}

	jiraTransition := []jira.Transition{
		{
			Name: "Verified",
			ID:   "123",
			To:   jira.Status{Name: "Verified", Description: "The issues has been verified"},
		},
	}

	existingLinks := make(map[string][]jira.RemoteLink)
	existingLinks["OCPBUGS-123"] = remoteLink

	testCases := []struct {
		name                 string
		jiraFakeClientData   jiraFakeClientData
		gitHubFakeClientData gitHubFakeClientData
		issueToVerify        string
		tagName              string
		expected             expectedResult
		labelsError          error
	}{
		{
			name: "Missing QE-Approved label",
			jiraFakeClientData: jiraFakeClientData{
				issues:        []*jira.Issue{&onQAIssue},
				remoteLinks:   remoteLink,
				existingLinks: existingLinks,
				transitions:   jiraTransition,
			},
			gitHubFakeClientData: gitHubFakeClientData{issueLabelsExisting: []string{"openshift/vmware-vsphere-csi-driver-operator#105"}},
			issueToVerify:        "OCPBUGS-123",
			tagName:              "4.10",
			expected: expectedResult{
				errors:  nil,
				status:  "",
				message: "Fix included in accepted release 4.10\nJira issue will not be automatically moved to VERIFIED for the following reasons:\n- PR openshift/vmware-vsphere-csi-driver-operator#105 not approved by the QA Contact\n\nThis issue must now be manually moved to VERIFIED by Jack Smith",
			},
		},
		{
			name: "Move ON_QA to Verified",
			jiraFakeClientData: jiraFakeClientData{
				issues:        []*jira.Issue{&onQAIssue2},
				remoteLinks:   remoteLink,
				existingLinks: existingLinks,
				transitions:   jiraTransition,
			},
			gitHubFakeClientData: gitHubFakeClientData{issueLabelsExisting: []string{"openshift/vmware-vsphere-csi-driver-operator#105:qe-approved"}},
			issueToVerify:        "OCPBUGS-123",
			tagName:              "4.10",
			expected: expectedResult{
				errors:  nil,
				status:  "Verified",
				message: "Fix included in accepted release 4.10\nAll linked GitHub PRs have been approved by a QA contact; updating bug status to VERIFIED",
			},
		},
		{
			name: "Already verified Issue",
			jiraFakeClientData: jiraFakeClientData{
				issues:        []*jira.Issue{&verifiedIssue},
				remoteLinks:   remoteLink,
				existingLinks: existingLinks,
			},
			issueToVerify: "OCPBUGS-123",
			tagName:       "4.10",
			expected: expectedResult{
				errors:  nil,
				status:  "Verified",
				message: "",
			},
		},
		{
			name: "Issue in the wrong state",
			jiraFakeClientData: jiraFakeClientData{
				issues:        []*jira.Issue{&inProgressIssue},
				remoteLinks:   remoteLink,
				existingLinks: existingLinks,
			},
			issueToVerify: "OCPBUGS-123",
			tagName:       "4.10",
			expected: expectedResult{
				errors:  nil,
				status:  "In Progress",
				message: "Fix included in accepted release 4.10\nJira issue will not be automatically moved to VERIFIED for the following reasons:\n- issue is not in ON_QA status\n\nThis issue must now be manually moved to VERIFIED by Jack Smith",
			},
		},
		{
			name: "Wrong TagName",
			jiraFakeClientData: jiraFakeClientData{
				issues:        []*jira.Issue{&inProgressIssue2},
				remoteLinks:   remoteLink,
				existingLinks: existingLinks,
				transitions:   jiraTransition,
			},
			gitHubFakeClientData: gitHubFakeClientData{issueLabelsExisting: []string{"openshift/vmware-vsphere-csi-driver-operator#105"}},
			issueToVerify:        "OCPBUGS-123",
			tagName:              "4.12",
			expected: expectedResult{
				errors:  nil,
				status:  "In Progress",
				message: "",
			},
		},
		{
			name: "Fail to get PR",
			jiraFakeClientData: jiraFakeClientData{
				issues:        []*jira.Issue{&onQAIssue3},
				remoteLinks:   remoteLink,
				existingLinks: existingLinks,
				transitions:   jiraTransition,
			},
			issueToVerify: "OCPBUGS-123",
			tagName:       "4.10",
			expected: expectedResult{
				errors:  []error{errors.New("unable to get labels for github pull openshift/vmware-vsphere-csi-driver-operator#105: injected error")},
				status:  "ON_QA",
				message: "",
			},
			labelsError: errors.New("injected error"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jc := &fakejira.FakeClient{
				Issues:        tc.jiraFakeClientData.issues,
				RemovedLinks:  tc.jiraFakeClientData.remoteLinks,
				ExistingLinks: tc.jiraFakeClientData.existingLinks,
				Transitions:   tc.jiraFakeClientData.transitions,
			}
			upstreamFakeGH := &fakegithub.FakeClient{IssueLabelsExisting: tc.gitHubFakeClientData.issueLabelsExisting}
			gh := &fakeGHClient{GetIssueLabelsError: tc.labelsError, FakeClient: upstreamFakeGH}
			v := NewVerifier(jc, gh, &plugins.Configuration{})
			err := v.VerifyIssues([]string{tc.issueToVerify}, tc.tagName)
			if len(err) != len(tc.expected.errors) {
				t.Errorf("number of errors (%d) does not match expected number of errors (%d)", len(err), len(tc.expected.errors))
			}
			for index, actualError := range err {
				if index > len(tc.expected.errors)+1 {
					break
				}
				if actualError.Error() != tc.expected.errors[index].Error() {
					t.Errorf("Actual error (%s) does not match expected error (%s)", actualError.Error(), tc.expected.errors[index].Error())
				}
			}
			if tc.expected.status != "" {
				if jc.Issues[0].Fields.Status.Name != tc.expected.status {
					t.Errorf("Unexpected issues status. Expecting: %s, but got: %s", tc.expected.status, jc.Issues[0].Fields.Status.Name)
				}
			}
			if tc.expected.message != "" {
				foundExpectedComment := false
				for _, comment := range jc.Issues[0].Fields.Comments.Comments {
					if comment.Body == tc.expected.message && (comment.Author.Name == "") {
						foundExpectedComment = true
						break
					}
				}
				if !foundExpectedComment {
					t.Errorf("The issue is not commented as expected!")
				}
			} else {
				if len(jc.Issues[0].Fields.Comments.Comments) > 0 {
					t.Errorf("A comment was made when none were expected")
				}
			}
		})
	}
}

const onQAIssueJSON = `
{
  "key": "OCPBUGS-123",
  "fields": {
    "status": {
      "description": "Status ON_QA",
      "name": "ON_QA"
    },
    "customfield_12315948": {
      "name": "qa_contact@redhat.com",
      "key": "qa_contact",
      "emailAddress": "qa_contact@redhat.com",
      "displayName": "Jack Smith"
    },
    "customfield_12319940": [
      {
        "self": "https://issues.redhat.com/rest/api/2/version/12390168",
        "id": "12390168",
        "description": "Release Version",
        "name": "4.10.z"
      }
    ],
    "comment": {
      "comments": []
    }
  }
}
`

const verifiedIssueJSON = `
{
  "key": "OCPBUGS-123",
  "fields": {
    "status": {
      "description": "Issue is verified",
      "name": "Verified"
    },
    "customfield_12315948": {
      "name": "qa_contact@redhat.com",
      "key": "qa_contact",
      "emailAddress": "qa_contact@redhat.com",
      "displayName": "Jack Smith"
    },
    "customfield_12319940": [
      {
        "self": "https://issues.redhat.com/rest/api/2/version/12390168",
        "id": "12390168",
        "description": "Release Version",
        "name": "4.10.z"
      }
    ],
    "comment": {
      "comments": []
    }
  }
}
`

const inProgressIssueJSON = `
{
  "key": "OCPBUGS-123",
  "fields": {
    "status": {
      "description": "Issue is in progress",
      "name": "In Progress"
    },
    "customfield_12315948": {
      "name": "qa_contact@redhat.com",
      "key": "qa_contact",
      "emailAddress": "qa_contact@redhat.com",
      "displayName": "Jack Smith"
    },
    "customfield_12319940": [
      {
        "self": "https://issues.redhat.com/rest/api/2/version/12390168",
        "id": "12390168",
        "description": "Release Version",
        "name": "4.10.z"
      }
    ],
    "comment": {
      "comments": []
    }
  }
}
`

const remoteLinksJSON = `
[
  {
    "object": {
      "url": "https://github.com/openshift/vmware-vsphere-csi-driver-operator/pull/105"
    }
  }
]
`

package main

import "testing"

func TestProviderRouterCloseSessionWithoutOptionalCLIs(t *testing.T) {
	var claude *claudeCLIProvider
	var agy *agyProvider
	// Matches main's construction on a clean machine: these interfaces are
	// non-nil even though the concrete pointers inside them are nil.
	router := &providerRouter{claude: claude, agy: agy}
	router.CloseSession("unused-session")
}

func TestCLIProviderCloseSessionPreservesOtherConversations(t *testing.T) {
	claude := newClaudeCLIProvider("unused")
	agy := newAgyProvider("unused")
	for _, conversations := range []map[string]agyConversation{claude.conversations, agy.conversations} {
		conversations["closing"] = agyConversation{ID: "one"}
		conversations["retained"] = agyConversation{ID: "two"}
	}
	router := &providerRouter{claude: claude, agy: agy}
	router.CloseSession("closing")
	for _, conversations := range []map[string]agyConversation{claude.conversations, agy.conversations} {
		if len(conversations) != 1 || conversations["retained"].ID != "two" {
			t.Fatalf("unexpected remaining conversations: %#v", conversations)
		}
	}
}

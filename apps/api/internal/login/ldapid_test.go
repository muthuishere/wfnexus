package login

import "testing"

// The stable id is the key; the username attribute is required only when an
// entry has no stable id (an Active Directory entry with objectGUID and no
// uid signs in).
func TestLDAPIdentityPrefersTheStableID(t *testing.T) {
	for _, c := range []struct {
		name, canonical, uuid string
		guid                  []byte
		subject, username     string
		fails                 bool
	}{
		{"entryUUID", "ana", "AB-12", nil, "entryUUID:ab-12", "ana", false},
		{"AD objectGUID, no uid", "", "", []byte{0xde, 0xad}, "objectGUID:dead", "Typed.Name", false},
		{"uid only", "ana", "", nil, "uid:ana", "ana", false},
		{"nothing", "", "", nil, "", "", true},
	} {
		id, err := ldapIdentity("corp", "uid", "Typed.Name", c.canonical, c.uuid, c.guid, nil)
		if c.fails {
			if err == nil {
				t.Errorf("%s: accepted", c.name)
			}
			continue
		}
		if err != nil || id.Subject != c.subject || id.Username != c.username {
			t.Errorf("%s: %+v %v", c.name, id, err)
		}
	}
}

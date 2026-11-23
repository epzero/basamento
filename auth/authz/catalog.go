package authz

import "slices"

type class string

const (
	classRead  class = "read"
	classWrite class = "write"
	classUse   class = "use"
)

// Role is one of the three built-in roles.
type Role string

const (
	RoleViewer Role = "viewer"
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
)

type catalogEntry struct {
	kind  string
	class class
}

var catalog = map[string]catalogEntry{
	"run.list":               {kind: "project", class: classRead},
	"run.get":                {kind: "run", class: classRead},
	"invocation.get":         {kind: "invocation", class: classRead},
	"invocation.events":      {kind: "invocation", class: classRead},
	"stream.attach":          {kind: "run", class: classRead},
	"stream.reattach":        {kind: "run", class: classRead},
	"stream.detach":          {kind: "run", class: classRead},
	"browser.session.read":   {kind: "browser session", class: classRead},
	"pool.list":              {kind: "tenant", class: classRead},
	"worker.list":            {kind: "pool", class: classRead},
	"binding.list":           {kind: "project", class: classRead},
	"run.start":              {kind: "run", class: classWrite},
	"run.cancel":             {kind: "run", class: classWrite},
	"gate.resolve":           {kind: "gate", class: classWrite},
	"conversation.send":      {kind: "conversation", class: classWrite},
	"invocation.start":       {kind: "invocation", class: classWrite},
	"invocation.cancel":      {kind: "invocation", class: classWrite},
	"browser.session.start":  {kind: "browser session", class: classWrite},
	"browser.session.cancel": {kind: "browser session", class: classWrite},
	"pool.join":              {kind: "pool", class: classWrite},
	"pool.leave":             {kind: "pool", class: classWrite},
	"binding.grant":          {kind: "project", class: classWrite},
	"binding.revoke":         {kind: "project", class: classWrite},
	"credential.names":       {kind: "project", class: classRead},
	"credential.put":         {kind: "credential name", class: classWrite},
	"credential.delete":      {kind: "credential name", class: classWrite},
	"credential.resolve":     {kind: "credential name", class: classUse},
	"worker.remove":          {kind: "worker", class: classWrite},
	"audit.read":             {kind: "tenant", class: classRead},
}

var viewerActions = []string{
	"run.list", "run.get", "invocation.get", "invocation.events",
	"stream.attach", "stream.reattach", "stream.detach",
	"browser.session.read", "pool.list", "worker.list", "binding.list",
}

var memberExtra = []string{
	"run.start", "run.cancel", "gate.resolve", "conversation.send",
	"invocation.start", "invocation.cancel",
	"browser.session.start", "browser.session.cancel",
	"pool.join", "pool.leave",
}

var adminExtra = []string{
	"binding.grant", "binding.revoke",
	"credential.names", "credential.put", "credential.delete", "credential.resolve",
	"worker.remove", "audit.read",
}

var rolePermissions = map[Role]PermissionSet{
	RoleViewer: newSet(viewerActions),
	RoleMember: newSet(append(append([]string{}, viewerActions...), memberExtra...)),
	RoleAdmin:  newSet(append(append(append([]string{}, viewerActions...), memberExtra...), adminExtra...)),
}

func newSet(actions []string) PermissionSet {
	out := make(PermissionSet, len(actions))
	for _, a := range actions {
		out[a] = struct{}{}
	}
	return out
}

func catalogHas(action string) bool {
	_, ok := catalog[action]
	return ok
}

// Actions returns every declared action in stable sorted order.
func Actions() []string {
	out := make([]string, 0, len(catalog))
	for a := range catalog {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

// Permissions returns a copy of the permissions granted to r.
func (r Role) Permissions() []string {
	ps, ok := rolePermissions[r]
	if !ok {
		return nil
	}
	return ps.slice()
}

// Roles returns the three built-in role names in order.
func Roles() []Role {
	return []Role{RoleViewer, RoleMember, RoleAdmin}
}

func rolePerms(r Role) PermissionSet {
	ps := rolePermissions[r]
	return ps.clone()
}

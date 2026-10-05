package authz

var GPTAccountRead = Permission{Resource: "gpt_account", Action: ActionRead}
var GPTAccountManage = Permission{Resource: "gpt_account", Action: "manage"}

func init() {
	RegisterResource(ResourceDefinition{Resource: "gpt_account", LabelKey: "GPT Accounts", Actions: []ActionDefinition{
		{Action: ActionRead, LabelKey: "View GPT accounts", DescriptionKey: "View managed GPT account status without secrets.", DefaultRoles: []string{BuiltInRoleAdmin}},
		{Action: "manage", LabelKey: "Manage GPT accounts", DescriptionKey: "Manage owned GPT accounts and authorize their channels.", DefaultRoles: []string{BuiltInRoleAdmin}},
	}})
}

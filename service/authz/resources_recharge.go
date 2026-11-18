package authz

const (
	ResourceBilling    = "billing"
	ResourceRedemption = "redemption"
)

var (
	BillingRead     = Permission{Resource: ResourceBilling, Action: ActionRead}
	BillingWrite    = Permission{Resource: ResourceBilling, Action: ActionWrite}
	RedemptionRead  = Permission{Resource: ResourceRedemption, Action: ActionRead}
	RedemptionWrite = Permission{Resource: ResourceRedemption, Action: ActionWrite}
)

// The recharge resources keep the names and actions of the access token scopes
// they replace, so stored scopes such as billing:read keep their meaning.
func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceBilling,
		LabelKey: "Billing management",
		Recharge: true,
		Actions: []ActionDefinition{
			{
				Action:         ActionRead,
				LabelKey:       "View recharge records",
				DescriptionKey: "View every user's top-up orders, recharge logs, recharge audit records, subscription plans, and user subscriptions.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionWrite,
				LabelKey:       "Recharge users and manage plans",
				DescriptionKey: "Complete top-up orders, add or subtract user quota, and manage subscription plans and user subscriptions.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
		},
	})
	RegisterResource(ResourceDefinition{
		Resource: ResourceRedemption,
		LabelKey: "Redemption codes",
		Recharge: true,
		Actions: []ActionDefinition{
			{
				Action:         ActionRead,
				LabelKey:       "View redemption codes",
				DescriptionKey: "View redemption codes, their quota, and who redeemed them.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionWrite,
				LabelKey:       "Manage redemption codes",
				DescriptionKey: "Create, edit, and delete redemption codes.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
		},
	})
}

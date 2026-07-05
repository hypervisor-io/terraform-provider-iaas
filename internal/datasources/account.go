package datasources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

// Interface assertions - account is a SINGLETON whoami data source, unlike
// every other data source in this package: it has no input filter attribute
// because there is exactly one object to read (the caller's own account), not
// a catalog to search by name. See internal/client/account.go for why it
// targets GET /profile rather than the plan's originally-named GET /connect.
var (
	_ datasource.DataSource              = &accountDataSource{}
	_ datasource.DataSourceWithConfigure = &accountDataSource{}
)

// NewAccountDataSource is the constructor registered with the provider.
func NewAccountDataSource() datasource.DataSource {
	return &accountDataSource{}
}

// accountDataSource resolves the authenticated caller's own account. Read
// calls GET /profile, which validates the configured token + IP-lock as a
// side effect (401/403 surface the shared IP-lock diagnostic hint via
// tfdiag.FromErr), so referencing this data source lets a configuration fail
// fast on a bad/expired/IP-mismatched token before any resource apply begins.
type accountDataSource struct {
	client *client.Client
}

// accountModel maps the data-source state. Every attribute is Computed - there
// is no input filter (singleton lookup, no Required/Optional attribute).
type accountModel struct {
	ID               types.String `tfsdk:"id"`
	FirstName        types.String `tfsdk:"first_name"`
	LastName         types.String `tfsdk:"last_name"`
	Email            types.String `tfsdk:"email"`
	CompanyName      types.String `tfsdk:"company_name"`
	Status           types.Int64  `tfsdk:"status"`
	IsAdmin          types.Bool   `tfsdk:"is_admin"`
	Timezone         types.String `tfsdk:"timezone"`
	DefaultCurrency  types.String `tfsdk:"default_currency"`
	TwoFactorEnabled types.Bool   `tfsdk:"two_factor_enabled"`
	SelfProvisioning types.Bool   `tfsdk:"self_provisioning"`
	OwnerID          types.String `tfsdk:"owner_id"`
	Gravatar         types.String `tfsdk:"gravatar"`
	LastLoginAt      types.String `tfsdk:"last_login_at"`
	CreatedAt        types.String `tfsdk:"created_at"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
}

func (d *accountDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_account"
}

func (d *accountDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Returns the authenticated caller's own account (whoami). This is a " +
			"SINGLETON data source - it has no input filter, since there is exactly one " +
			"account behind the configured provider token. Referencing it (e.g. as " +
			"`data.iaas_account.current`) validates the token and IP-lock during plan/apply, " +
			"letting a misconfigured provider fail fast, and exposes `id` for use elsewhere in " +
			"a configuration.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the authenticated account.",
			},
			"first_name": schema.StringAttribute{
				Computed:    true,
				Description: "Account holder's first name.",
			},
			"last_name": schema.StringAttribute{
				Computed:    true,
				Description: "Account holder's last name.",
			},
			"email": schema.StringAttribute{
				Computed:    true,
				Description: "Account email address.",
			},
			"company_name": schema.StringAttribute{
				Computed:    true,
				Description: "Company name on the account, if set.",
			},
			"status": schema.Int64Attribute{
				Computed:    true,
				Description: "Account status code.",
			},
			"is_admin": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the authenticated account is a master-panel admin.",
			},
			"timezone": schema.StringAttribute{
				Computed:    true,
				Description: "Account's configured timezone (e.g. `America/New_York`).",
			},
			"default_currency": schema.StringAttribute{
				Computed:    true,
				Description: "Account's default billing currency code (e.g. `USD`).",
			},
			"two_factor_enabled": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether two-factor authentication (TOTP) is enabled on the account.",
			},
			"self_provisioning": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether self-provisioning resource packs are enabled for the account.",
			},
			"owner_id": schema.StringAttribute{
				Computed: true,
				Description: "UUID of the account-owning user. Empty when the authenticated " +
					"account IS the owner; non-empty means the token belongs to a subuser of " +
					"that owner account.",
			},
			"gravatar": schema.StringAttribute{
				Computed:    true,
				Description: "Gravatar URL derived from the account email.",
			},
			"last_login_at": schema.StringAttribute{
				Computed:    true,
				Description: "Timestamp of the account's last login.",
			},
			"created_at": schema.StringAttribute{
				Computed:    true,
				Description: "Timestamp the account was created.",
			},
			"updated_at": schema.StringAttribute{
				Computed:    true,
				Description: "Timestamp the account was last updated.",
			},
		},
	}
}

// Configure pulls the shared *client.Client from the provider (nil-guard +
// typed-mismatch error), identically to every other data source.
func (d *accountDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read calls GET /profile and maps the returned account object onto the
// singleton data-source state. There is no config input to read (no filter
// attribute exists).
func (d *accountDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	account, err := d.client.GetAccount(ctx)
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error reading account", err))
		return
	}

	state := accountModel{
		ID:               types.StringValue(strField(account, "id")),
		FirstName:        types.StringValue(strField(account, "first_name")),
		LastName:         types.StringValue(strField(account, "last_name")),
		Email:            types.StringValue(strField(account, "email")),
		CompanyName:      types.StringValue(strField(account, "company_name")),
		Status:           types.Int64Value(int64Field(account, "status")),
		IsAdmin:          types.BoolValue(boolField(account, "is_admin")),
		Timezone:         types.StringValue(strField(account, "timezone")),
		DefaultCurrency:  types.StringValue(strField(account, "default_currency")),
		TwoFactorEnabled: types.BoolValue(boolField(account, "two_factor_enabled")),
		SelfProvisioning: types.BoolValue(boolField(account, "self_provisioning")),
		OwnerID:          types.StringValue(strField(account, "owner_id")),
		Gravatar:         types.StringValue(strField(account, "gravatar")),
		LastLoginAt:      types.StringValue(strField(account, "last_login_at")),
		CreatedAt:        types.StringValue(strField(account, "created_at")),
		UpdatedAt:        types.StringValue(strField(account, "updated_at")),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

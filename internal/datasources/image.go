package datasources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hypervisor-io/terraform-provider-iaas/client"
	"github.com/hypervisor-io/terraform-provider-iaas/internal/tfdiag"
)

var (
	_ datasource.DataSource              = &imageDataSource{}
	_ datasource.DataSourceWithConfigure = &imageDataSource{}
)

// NewImageDataSource is the constructor registered with the provider.
func NewImageDataSource() datasource.DataSource {
	return &imageDataSource{}
}

// imageDataSource looks up an OS image by name. The image-search endpoint
// returns a Select2 grouped envelope; the client flattens it to a list of
// children, each carrying id/text/distro. This data source matches a child by
// its text, preferring an EXACT match before falling back to a unique
// case-insensitive substring match.
type imageDataSource struct {
	client *client.Client
}

// imageModel maps the data-source state. name is the input filter,
// hypervisor_group_id an optional search scope; the rest are computed.
type imageModel struct {
	Name              types.String `tfsdk:"name"`
	HypervisorGroupID types.String `tfsdk:"hypervisor_group_id"`
	ID                types.String `tfsdk:"id"`
	Distro            types.String `tfsdk:"distro"`
}

func (d *imageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

func (d *imageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an OS image by name via the image-search catalogue. Matches " +
			"the image's display text exactly first, then falls back to a unique " +
			"case-insensitive substring match. Exactly one image must resolve.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required: true,
				Description: "Name of the image to look up (e.g. `Ubuntu 24.04`). Matched " +
					"against the catalogue's display text. Exactly one image must resolve.",
			},
			"hypervisor_group_id": schema.StringAttribute{
				Optional: true,
				Description: "Optional hypervisor group (location) UUID to scope the search to " +
					"images available at that location.",
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "UUID of the matched image.",
			},
			"distro": schema.StringAttribute{
				Computed:    true,
				Description: "Distribution family of the matched image (e.g. `ubuntu`).",
			},
		},
	}
}

func (d *imageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, problem := configureClient(req.ProviderData)
	if problem != "" {
		resp.Diagnostics.AddError("Unexpected Provider Data Type", problem)
		return
	}
	d.client = c
}

// Read searches the image catalogue and resolves a single image. Exact text
// match wins; otherwise a unique case-insensitive substring match is accepted.
// Zero / multiple matches are an error.
func (d *imageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg imageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()

	images, err := d.client.SearchImages(ctx, name, cfg.HypervisorGroupID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(tfdiag.FromErr("Error searching images", err))
		return
	}

	match, err := resolveImage(images, name)
	if err != nil {
		resp.Diagnostics.AddError("Image lookup failed", err.Error())
		return
	}

	cfg.ID = types.StringValue(strField(match, "id"))
	cfg.Distro = types.StringValue(strField(match, "distro"))

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// imageText returns the human label for an image child, preferring "text"
// (Select2) and falling back to "name".
func imageText(img map[string]any) string {
	if t := strField(img, "text"); t != "" {
		return t
	}
	return strField(img, "name")
}

// resolveImage picks the single image matching name. An EXACT text match is
// preferred and wins outright (even if substrings also match). Failing an exact
// match, a unique case-insensitive substring match is accepted; zero or multiple
// substring matches are an error.
func resolveImage(images []map[string]any, name string) (map[string]any, error) {
	// Prefer exact match - short-circuits ambiguity (e.g. "Ubuntu 24.04" vs
	// "Ubuntu 24.04 Minimal").
	exact, exactErr := findUnique(images, "image", name, func(img map[string]any) bool {
		return imageText(img) == name
	})
	if exactErr == nil {
		return exact, nil
	}
	// Only fall back to substring matching when there was NO exact match.
	// A "multiple exact matches" error must be surfaced, not masked by substring.
	if !strings.HasPrefix(exactErr.Error(), "no ") {
		return nil, exactErr
	}

	// Fall back to a unique case-insensitive substring match.
	lower := strings.ToLower(name)
	var found []map[string]any
	for _, img := range images {
		if strings.Contains(strings.ToLower(imageText(img)), lower) {
			found = append(found, img)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, fmt.Errorf("no image matching name %q", name)
	default:
		return nil, fmt.Errorf("multiple image match name %q; refine your filter", name)
	}
}

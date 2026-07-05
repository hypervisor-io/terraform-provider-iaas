# Docs site

`index.html` is a single, self-contained static page (no external CSS/JS/fonts/CDN - everything is
inlined) documenting the `iaas` OpenTofu / Terraform provider for end users and admins: getting
started, the IP-locked auth model, the users-vs-admins token model, the full 56-type resource/data-source
catalog with a search/filter box, and common patterns (async convergence, import, troubleshooting).

It is a plain static file - no build step, no dependencies, nothing to `npm install`.

## Enable GitHub Pages

Once this repo (or a public mirror of it) is pushed to GitHub, turn the page on with either option:

**Option A - serve straight from `/site` (no extra step):**

1. Go to the repo's **Settings → Pages**.
2. Under **Build and deployment**, set **Source** to **Deploy from a branch**.
3. Pick the branch (e.g. `main`) and folder **`/site`** (GitHub's folder dropdown will show `/site` if the
   directory exists at the repo root and contains an `index.html`).
4. Save. GitHub publishes to `https://<org-or-user>.github.io/<repo>/` within a minute or two.

**Option B - move the folder to `/docs` (GitHub Pages' other supported folder):**

```sh
git mv site docs
git commit -m "docs: move provider docs site to /docs for GitHub Pages"
```

Then in **Settings → Pages**, set the source folder to **`/docs`** instead of `/site` (GitHub Pages
only supports serving from the repository root or a top-level `/docs` folder - `/site` only works if
your GitHub UI's branch/folder picker exposes it directly, which recent GitHub versions do; `/docs` is
the more universally-supported fallback if it doesn't).

## Updating the page

Edit `index.html` directly and re-deploy (GitHub Pages redeploys automatically on every push to the
configured branch/folder). There is no build/compile step - it's just HTML/CSS/JS in one file.

## Notes

- This file was **not** committed or pushed as part of generating it - review the diff first.
- Nothing in this repo was pushed or had GitHub Pages toggled on automatically; enabling Pages is a
  manual step in the repo's Settings, per above.

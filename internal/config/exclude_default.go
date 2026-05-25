package config

// DefaultExcludeContent is the file that gets written to
// ~/.blamely/exclude on first install. Users can edit it freely; Blamely
// only reads it, never rewrites unless the file is missing.
//
// Syntax (subset of .gitignore):
//
//	name        — any path component named `name` (e.g. `target` matches
//	              `target/foo.class` and `app/target/build.log`)
//	name/       — same as `name` (trailing slash kept for readability)
//	*.ext       — basename glob via filepath.Match (e.g. `*.class`)
//	/name       — anchored at repo root; matches `name` or `name/...`
//	/name/      — anchored directory; matches anything under `name/`
//	# comment   — lines starting with `#` are ignored
//	(blank)     — blank lines are ignored
//
// The patterns below cover the build outputs and dependency directories
// that almost every project wants out of attribution. Add project-specific
// entries by editing the file.
const DefaultExcludeContent = `# Blamely exclude list — files matching these patterns are skipped at
# diff time and never appear in attribution or commit reports.
#
# Syntax (subset of .gitignore):
#   name         any path component named "name" (e.g. target/foo.class)
#   name/        same as above, trailing slash kept for readability
#   *.ext        basename glob
#   /name        anchored at repo root
#   # comment    ignored
#
# Edit this file freely. Blamely reads it but never overwrites it once it
# exists. The post-commit hook reloads it on every commit, so changes take
# effect immediately.

# ---- Build outputs ---------------------------------------------------------
target/
build/
out/
dist/
bin/
obj/
.gradle/
.next/
.nuxt/
.output/
.svelte-kit/
.turbo/
.parcel-cache/
cmake-build-*/
DerivedData/

# ---- Dependencies ----------------------------------------------------------
node_modules/
vendor/
.venv/
venv/
.bundle/
Pods/

# ---- Caches & test artifacts ----------------------------------------------
.cache/
__pycache__/
.pytest_cache/
.mypy_cache/
.ruff_cache/
.tox/
coverage/
.nyc_output/
.coverage
htmlcov/

# ---- IDE / editor state ----------------------------------------------------
.idea/
.vscode/
.vs/
*.iml
*.sln.iml

# ---- Lockfiles / generated manifests --------------------------------------
package-lock.json
yarn.lock
pnpm-lock.yaml
bun.lockb
Pipfile.lock
poetry.lock
Cargo.lock
Gemfile.lock
composer.lock
go.sum

# ---- Compiled artifacts ----------------------------------------------------
*.class
*.jar
*.war
*.ear
*.pyc
*.pyo
*.o
*.so
*.dylib
*.dll
*.exe
*.a

# ---- Minified / bundled web assets ----------------------------------------
*.min.js
*.min.css
*.map
*.bundle.js
*.chunk.js

# ---- Media & binary files --------------------------------------------------
*.png
*.jpg
*.jpeg
*.gif
*.svg
*.ico
*.webp
*.pdf
*.zip
*.tar
*.gz
*.7z
*.mp3
*.mp4
*.mov
*.woff
*.woff2
*.ttf
*.otf
*.eot

# ---- OS junk ---------------------------------------------------------------
.DS_Store
Thumbs.db

# ---- Blamely's own state ---------------------------------------------------
.blamely/
`

# Private-upstream drill

Status: committed procedure; external-service run not yet recorded.

This drill validates a disposable private npm package and OCI image through
n0ding with two authorized identities, denied access, revocation/logout, real
clients, redirects as observed by the provider, and a credential-canary scan.
It does not publish through n0ding and does not turn the fixture into a public
release.

The deterministic companion test is
`TestPrivateUpstreamDrill` in
`internal/httpserver/private_upstream_drill_test.go`. It runs in CI with local
fake registries and covers the policy branches that cannot be made
provider-independent.

## Safety rules

- Create new, short-lived credentials for this drill. Never use a personal,
  production, or reusable token.
- Seed a uniquely named disposable package and image directly through the
  chosen providers before the drill. n0ding remains read-only.
- Give identities A and B read access only. The denied identity must have no
  read access.
- Keep every output under ignored `.tmp/private-upstream-drill`. Do not commit
  it, paste it into an issue, or attach it to CI.
- Put no credential in n0ding's config, command line, image, or repository.
  Client credentials are supplied only from environment variables/stdin.
- Stop immediately if a client prints a credential or the canary scan reports
  a finding. Preserve access restrictions, revoke all drill credentials, and
  investigate before sharing any output.

## Required disposable setup

Use names carrying both a drill prefix and a random suffix, for example:

```powershell
$suffix = [Guid]::NewGuid().ToString("N").Substring(0, 12)
"@my-drill-scope/n0ding-private-$suffix"
"my-drill-namespace/n0ding-private-$suffix:1"
```

Create those resources directly on the selected private npm/OCI services.
Record the provider, region/tenant, cleanup owner, and expiry time in a private
work note, not in Git.

Set these environment variables in a temporary PowerShell session:

| Variable | Meaning |
|---|---|
| `N0DING_NPM_UPSTREAM` | Absolute npm registry base URL, without credentials or query |
| `N0DING_NPM_PACKAGE` | Disposable scoped private package name |
| `N0DING_NPM_TOKEN_A` | Disposable read token for identity A |
| `N0DING_NPM_TOKEN_B` | Distinct disposable read token for identity B |
| `N0DING_NPM_TOKEN_DENIED` | Conspicuous invalid/revoked token, at least 8 characters |
| `N0DING_OCI_UPSTREAM` | Absolute OCI registry base URL, without credentials or query |
| `N0DING_OCI_IMAGE` | Disposable private image name and tag, excluding n0ding host |
| `N0DING_OCI_USER_A` / `N0DING_OCI_PASSWORD_A` | Disposable OCI identity A |
| `N0DING_OCI_USER_B` / `N0DING_OCI_PASSWORD_B` | Distinct disposable OCI identity B |
| `N0DING_OCI_USER_DENIED` / `N0DING_OCI_PASSWORD_DENIED` | Denied disposable identity |
| `N0DING_QUERY_CANARY` | Random non-credential query canary, at least 8 characters |

Do not add these values to `.env`; that filename is ignored only as a final
guard, not as a secrets workflow. Check all variables without printing them:

```powershell
$required = @(
  "N0DING_NPM_UPSTREAM", "N0DING_NPM_PACKAGE",
  "N0DING_NPM_TOKEN_A", "N0DING_NPM_TOKEN_B",
  "N0DING_NPM_TOKEN_DENIED", "N0DING_OCI_UPSTREAM",
  "N0DING_OCI_IMAGE", "N0DING_OCI_USER_A",
  "N0DING_OCI_PASSWORD_A", "N0DING_OCI_USER_B",
  "N0DING_OCI_PASSWORD_B", "N0DING_OCI_USER_DENIED",
  "N0DING_OCI_PASSWORD_DENIED", "N0DING_QUERY_CANARY"
)
foreach ($name in $required) {
  $value = [Environment]::GetEnvironmentVariable($name)
  if ([string]::IsNullOrWhiteSpace($value)) {
    throw "Missing required environment variable: $name"
  }
}
```

## Start an isolated n0ding

The committed drill config contains only environment placeholders. Build the
local test image and set the non-secret runtime values:

```powershell
$env:N0DING_PUBLIC_URL = "http://127.0.0.1:18080"
$env:N0DING_DRILL_CACHE_PATH = "/data"
docker build --tag n0ding:private-drill .
```

Validate the drill file with a temporary bind mount, then start the isolated
server:

```powershell
$root = (Get-Location).Path
$drillConfig = Join-Path $root "config\n0ding.private-drill.toml"
$evidence = Join-Path $root ".tmp\private-upstream-drill\evidence"
$npmClient = Join-Path $root ".tmp\private-upstream-drill\npm-client"
New-Item -ItemType Directory -Force -Path $evidence, $npmClient | Out-Null

docker run --rm `
  --env N0DING_PUBLIC_URL `
  --env N0DING_DRILL_CACHE_PATH `
  --env N0DING_NPM_UPSTREAM `
  --env N0DING_OCI_UPSTREAM `
  --mount "type=bind,src=$drillConfig,dst=/drill.toml,readonly" `
  --entrypoint /usr/local/bin/n0ding n0ding:private-drill `
  -config /drill.toml -check-config

docker network create n0ding-private-drill
docker volume create n0ding-private-drill-data
docker run --detach --name n0ding-private-drill-server `
  --network n0ding-private-drill --network-alias n0ding `
  --publish 127.0.0.1:18080:8080 `
  --env N0DING_PUBLIC_URL `
  --env N0DING_DRILL_CACHE_PATH `
  --env N0DING_NPM_UPSTREAM `
  --env N0DING_OCI_UPSTREAM `
  --mount "type=bind,src=$drillConfig,dst=/drill.toml,readonly" `
  --volume n0ding-private-drill-data:/data `
  n0ding:private-drill -config /drill.toml

Invoke-RestMethod http://127.0.0.1:18080/healthz
```

Config validation does not contact either upstream.

## npm: A, B, denied, logout, and revocation

Create one temporary npm config containing an environment reference, not a
token value:

```powershell
@'
registry=http://127.0.0.1:18080/npm/
always-auth=true
//127.0.0.1:18080/npm/:_authToken=${N0DING_NPM_TOKEN}
'@ | Set-Content -LiteralPath (Join-Path $npmClient ".npmrc")

$npm = (Get-Command npm).Source
$npmConfig = Join-Path $npmClient ".npmrc"
```

Run two authorized identities with separate empty npm client caches:

```powershell
$env:N0DING_NPM_TOKEN = $env:N0DING_NPM_TOKEN_A
& $npm view $env:N0DING_NPM_PACKAGE version `
  --userconfig $npmConfig `
  --cache (Join-Path $npmClient "cache-a") --prefer-online
if ($LASTEXITCODE -ne 0) { throw "npm identity A was not authorized" }

$env:N0DING_NPM_TOKEN = $env:N0DING_NPM_TOKEN_B
& $npm view $env:N0DING_NPM_PACKAGE version `
  --userconfig $npmConfig `
  --cache (Join-Path $npmClient "cache-b") --prefer-online
if ($LASTEXITCODE -ne 0) { throw "npm identity B was not authorized" }
```

Both must return the expected package version. `/api/v1/status` must show npm
misses but zero npm cache objects in this fresh drill volume; authenticated npm
responses are intentionally never persisted.

Exercise denied access and capture the generic client error:

```powershell
$env:N0DING_NPM_TOKEN = $env:N0DING_NPM_TOKEN_DENIED
& $npm view $env:N0DING_NPM_PACKAGE version `
  --userconfig $npmConfig `
  --cache (Join-Path $npmClient "cache-denied") --prefer-online `
  2>&1 | Tee-Object -FilePath (Join-Path $evidence "npm-denied.txt")
if ($LASTEXITCODE -eq 0) { throw "denied npm identity unexpectedly succeeded" }
```

Logout is modeled by removing the client credential. Create a second temporary
config with no auth line and confirm anonymous access fails:

```powershell
@'
registry=http://127.0.0.1:18080/npm/
'@ | Set-Content -LiteralPath (Join-Path $npmClient "logged-out.npmrc")
Remove-Item Env:N0DING_NPM_TOKEN -ErrorAction SilentlyContinue
& $npm view $env:N0DING_NPM_PACKAGE version `
  --userconfig (Join-Path $npmClient "logged-out.npmrc") `
  --cache (Join-Path $npmClient "cache-logged-out") --prefer-online `
  2>&1 | Tee-Object -FilePath (Join-Path $evidence "npm-logged-out.txt")
if ($LASTEXITCODE -eq 0) { throw "logged-out npm client unexpectedly succeeded" }
```

Now revoke token A at the provider without restarting n0ding. Record the
provider revocation timestamp privately, wait only the provider's documented
minimum propagation interval, and repeat:

```powershell
$env:N0DING_NPM_TOKEN = $env:N0DING_NPM_TOKEN_A
& $npm view $env:N0DING_NPM_PACKAGE version `
  --userconfig $npmConfig `
  --cache (Join-Path $npmClient "cache-a-revoked") --prefer-online `
  2>&1 | Tee-Object -FilePath (Join-Path $evidence "npm-revoked.txt")
if ($LASTEXITCODE -eq 0) { throw "revoked npm identity unexpectedly succeeded" }
```

Finally send a query canary on an authenticated request. The canary may reach
the configured upstream but must not appear in persisted/operator output:

```powershell
$encodedPackage = [Uri]::EscapeDataString($env:N0DING_NPM_PACKAGE)
$headers = @{ Authorization = "Bearer $env:N0DING_NPM_TOKEN_B" }
Invoke-WebRequest `
  -Uri "http://127.0.0.1:18080/npm/$encodedPackage`?access_token=$env:N0DING_QUERY_CANARY" `
  -Headers $headers -SkipHttpErrorCheck | Out-Null
```

## OCI: A, B, denied, logout, revocation, and redirects

Use separate disposable Docker-in-Docker daemons so neither identity can reuse
the other's Docker content store:

```powershell
docker run --detach --privileged --name n0ding-private-drill-oci-a `
  --network n0ding-private-drill docker:29-dind `
  --insecure-registry=n0ding:8080
docker run --detach --privileged --name n0ding-private-drill-oci-b `
  --network n0ding-private-drill docker:29-dind `
  --insecure-registry=n0ding:8080
docker run --detach --privileged --name n0ding-private-drill-oci-denied `
  --network n0ding-private-drill docker:29-dind `
  --insecure-registry=n0ding:8080

$dindNames = @(
  "n0ding-private-drill-oci-a",
  "n0ding-private-drill-oci-b",
  "n0ding-private-drill-oci-denied"
)
foreach ($name in $dindNames) {
  $ready = $false
  for ($attempt = 0; $attempt -lt 30; $attempt++) {
    docker exec $name docker info *> $null
    if ($LASTEXITCODE -eq 0) {
      $ready = $true
      break
    }
    Start-Sleep -Seconds 1
  }
  if (-not $ready) { throw "Docker daemon did not become ready: $name" }
}

$env:N0DING_OCI_PASSWORD_A | docker exec -i `
  n0ding-private-drill-oci-a docker login n0ding:8080 `
  --username $env:N0DING_OCI_USER_A --password-stdin
if ($LASTEXITCODE -ne 0) { throw "OCI identity A login failed" }

$env:N0DING_OCI_PASSWORD_B | docker exec -i `
  n0ding-private-drill-oci-b docker login n0ding:8080 `
  --username $env:N0DING_OCI_USER_B --password-stdin
if ($LASTEXITCODE -ne 0) { throw "OCI identity B login failed" }
```

Pull with both identities and confirm the same provider digest:

```powershell
$throughN0ding = "n0ding:8080/$env:N0DING_OCI_IMAGE"
docker exec n0ding-private-drill-oci-a docker pull $throughN0ding
if ($LASTEXITCODE -ne 0) { throw "OCI identity A pull failed" }
$digestA = docker exec n0ding-private-drill-oci-a docker image inspect `
  $throughN0ding --format '{{index .RepoDigests 0}}'

docker exec n0ding-private-drill-oci-b docker pull $throughN0ding
if ($LASTEXITCODE -ne 0) { throw "OCI identity B pull failed" }
$digestB = docker exec n0ding-private-drill-oci-b docker image inspect `
  $throughN0ding --format '{{index .RepoDigests 0}}'
if ($digestA -ne $digestB) { throw "OCI identity digests differ" }
```

The second pull should increase OCI cache hits. Each hit is valid only if the
provider accepted that identity's current credential and returned the exact
cached digest on n0ding's internal `HEAD`. Provider/CDN redirect destinations
must be read from provider access logs or a private capture if available;
record whether the chain crossed origin. Do not capture or commit auth headers.
The deterministic local test separately proves that cross-origin destinations
receive no `Authorization`.

The denied login and pull must both fail:

```powershell
$env:N0DING_OCI_PASSWORD_DENIED | docker exec -i `
  n0ding-private-drill-oci-denied docker login n0ding:8080 `
  --username $env:N0DING_OCI_USER_DENIED --password-stdin `
  2>&1 | Tee-Object -FilePath (Join-Path $evidence "oci-denied-login.txt")
if ($LASTEXITCODE -eq 0) { throw "denied OCI login unexpectedly succeeded" }

docker exec n0ding-private-drill-oci-denied docker pull $throughN0ding `
  2>&1 | Tee-Object -FilePath (Join-Path $evidence "oci-denied-pull.txt")
if ($LASTEXITCODE -eq 0) { throw "denied OCI pull unexpectedly succeeded" }
```

Revoke identity B at the provider without restarting n0ding. Remove only the
disposable local image, then pull again:

```powershell
docker exec n0ding-private-drill-oci-b docker image rm --force $throughN0ding
docker exec n0ding-private-drill-oci-b docker pull $throughN0ding `
  2>&1 | Tee-Object -FilePath (Join-Path $evidence "oci-revoked.txt")
if ($LASTEXITCODE -eq 0) {
  throw "revoked OCI identity still succeeded; record provider token TTL/latency"
}
```

Some providers issue bearer tokens that remain valid until expiry after the
underlying login is revoked. If the pull succeeds, this gate is not passed:
record the observed TTL privately and repeat after expiry. Do not reinterpret
that result as a n0ding cache hit bypass; n0ding still performs the upstream
authorization `HEAD`.

For the separate logout signal, log identity A out locally, remove only the
disposable image, and confirm the anonymous pull fails:

```powershell
docker exec n0ding-private-drill-oci-a docker logout n0ding:8080
docker exec n0ding-private-drill-oci-a docker image rm --force $throughN0ding
docker exec n0ding-private-drill-oci-a docker pull $throughN0ding `
  2>&1 | Tee-Object -FilePath (Join-Path $evidence "oci-logged-out.txt")
if ($LASTEXITCODE -eq 0) { throw "logged-out OCI client unexpectedly succeeded" }
```

## Evidence and credential-canary scan

Capture operator-visible outputs without request headers:

```powershell
Invoke-WebRequest http://127.0.0.1:18080/api/v1/status `
  -OutFile (Join-Path $evidence "status.json")
Invoke-WebRequest http://127.0.0.1:18080/metrics `
  -OutFile (Join-Path $evidence "metrics.txt")
docker logs n0ding-private-drill-server 2>&1 |
  Set-Content -LiteralPath (Join-Path $evidence "n0ding.log")
```

Stop n0ding before copying complete cache artifacts:

```powershell
docker stop n0ding-private-drill-server
$cacheEvidence = Join-Path $evidence "cache-copy"
New-Item -ItemType Directory -Force -Path $cacheEvidence | Out-Null
docker run --rm --user 0:0 --entrypoint /bin/sh `
  --volume n0ding-private-drill-data:/source:ro `
  --mount "type=bind,src=$cacheEvidence,dst=/evidence" `
  n0ding:private-drill -c 'cp -a /source/. /evidence/'
```

Scan tokens, passwords, usernames, and the query canary as streaming UTF-8
text (with BOM-aware decoding). The scanner works across chunk boundaries and
prints variable names and affected paths, never values:

```powershell
$scanVariables = @(
  "N0DING_NPM_TOKEN_A", "N0DING_NPM_TOKEN_B",
  "N0DING_NPM_TOKEN_DENIED", "N0DING_OCI_USER_A",
  "N0DING_OCI_PASSWORD_A", "N0DING_OCI_USER_B",
  "N0DING_OCI_PASSWORD_B", "N0DING_OCI_USER_DENIED",
  "N0DING_OCI_PASSWORD_DENIED", "N0DING_QUERY_CANARY"
)
& .\tools\private-canary-scan.ps1 `
  -LiteralPath $evidence `
  -CanaryEnv $scanVariables
if (-not $?) { throw "credential canary scan failed" }
```

Expected pass signals:

- npm A and B succeed with distinct credentials; denied, logged-out, and
  revoked A fail; npm cache objects stay at zero.
- OCI A and B pull the same digest; B creates cache hits only after upstream
  `HEAD` authorization; denied, logged-out, and revoked pulls fail.
- A changed or missing digest is not assumed from a real provider. That branch
  is deterministic fixture evidence and must still pass in `go test`.
- Provider redirect logs show the actual chain without exposing headers; the
  local fixture proves cross-origin credential stripping.
- cache copy, status, metrics, logs, and captured client errors pass the
  canary scanner.

Any successful denied/revoked operation, mismatched digest, credential finding,
or unexplained lack of an OCI authorization `HEAD` is a failure.

## Cleanup

Revoke/delete every disposable token and identity first. Delete the disposable
package and image directly at their providers using the provider's normal
administrative path. Then remove only the explicitly named local resources:

```powershell
docker rm --force n0ding-private-drill-oci-a `
  n0ding-private-drill-oci-b n0ding-private-drill-oci-denied `
  n0ding-private-drill-server
docker network rm n0ding-private-drill
docker volume rm n0ding-private-drill-data
docker image rm n0ding:private-drill

Remove-Item -LiteralPath (Join-Path $root ".tmp\private-upstream-drill") `
  -Recurse -Force
foreach ($name in $required + @("N0DING_NPM_TOKEN")) {
  Remove-Item "Env:$name" -ErrorAction SilentlyContinue
}
Remove-Item Env:N0DING_PUBLIC_URL -ErrorAction SilentlyContinue
Remove-Item Env:N0DING_DRILL_CACHE_PATH -ErrorAction SilentlyContinue
```

Verify provider deletion and local cleanup in the private work note. Do not
commit that note or the drill evidence.

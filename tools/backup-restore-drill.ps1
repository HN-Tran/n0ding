[CmdletBinding()]
param(
    [string]$EvidenceRoot
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repositoryRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$composeFile = Join-Path $repositoryRoot "testdata/backup-restore/compose.yaml"
$sourceConfig = Join-Path $repositoryRoot "testdata/backup-restore/n0ding.toml"
$npmFixture = Join-Path $repositoryRoot "testdata/backup-restore/npm"
$canaryScanner = Join-Path $repositoryRoot "tools/private-canary-scan.ps1"
$suffix = [Guid]::NewGuid().ToString("N").Substring(0, 10)

if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $repositoryRoot ".tmp/backup-restore-drill/$suffix"
} else {
    $EvidenceRoot = [IO.Path]::GetFullPath($EvidenceRoot)
}
if (Test-Path -LiteralPath $EvidenceRoot) {
    throw "Evidence path already exists; refusing to overwrite it: $EvidenceRoot"
}
New-Item -ItemType Directory -Path $EvidenceRoot | Out-Null

$sourceProject = "n0ding-backup-$suffix-source"
$restoredProject = "n0ding-backup-$suffix-restored"
$corruptProject = "n0ding-backup-$suffix-corrupt"
$sourceVolume = "n0ding-backup-$suffix-source-data"
$restoredVolume = "n0ding-backup-$suffix-restored-data"
$corruptVolume = "n0ding-backup-$suffix-corrupt-data"
$n0dingImage = "n0ding:backup-restore-$suffix"
$fixtureImage = "n0ding-fixture:backup-restore-$suffix"
$createdVolumes = [Collections.Generic.List[string]]::new()
$projects = @(
    [pscustomobject]@{ Name = $sourceProject; Volume = $sourceVolume; Config = $sourceConfig },
    [pscustomobject]@{ Name = $restoredProject; Volume = $restoredVolume; Config = $null },
    [pscustomobject]@{ Name = $corruptProject; Volume = $corruptVolume; Config = $null }
)

$canaryValues = [ordered]@{
    N0DING_BACKUP_NPM_TOKEN_A        = "n0ding-backup-npm-token-a-canary"
    N0DING_BACKUP_NPM_TOKEN_B        = "n0ding-backup-npm-token-b-canary"
    N0DING_BACKUP_NPM_TOKEN_DENIED   = "n0ding-backup-npm-denied-canary"
    N0DING_BACKUP_OCI_TOKEN_A        = "n0ding-backup-oci-token-a-canary"
    N0DING_BACKUP_OCI_TOKEN_B        = "n0ding-backup-oci-token-b-canary"
    N0DING_BACKUP_OCI_TOKEN_DENIED   = "n0ding-backup-oci-denied-canary"
    N0DING_BACKUP_QUERY_CANARY       = "n0ding-backup-query-canary"
    N0DING_BACKUP_RESPONSE_CANARY    = "n0ding-backup-response-canary"
}
$managedEnvironment = @(
    "N0DING_DRILL_IMAGE",
    "N0DING_DRILL_FIXTURE_IMAGE",
    "N0DING_DRILL_CONFIG",
    "N0DING_DRILL_CACHE_VOLUME",
    "N0DING_DRILL_PORT"
) + @($canaryValues.Keys)
$previousEnvironment = @{}
foreach ($name in $managedEnvironment) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name)
}

function Invoke-Docker {
    param(
        [Parameter(Mandatory = $true)]
        [string[]]$Arguments,
        [string]$OutputPath
    )

    if ([string]::IsNullOrWhiteSpace($OutputPath)) {
        & docker @Arguments
    } else {
        & docker @Arguments *> $OutputPath
    }
    if ($LASTEXITCODE -ne 0) {
        $summary = ($Arguments | Select-Object -First 3) -join " "
        throw "Docker command failed ($LASTEXITCODE): docker $summary"
    }
}

function Set-StackEnvironment {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Volume,
        [Parameter(Mandatory = $true)]
        [string]$Config
    )

    $env:N0DING_DRILL_IMAGE = $n0dingImage
    $env:N0DING_DRILL_FIXTURE_IMAGE = $fixtureImage
    $env:N0DING_DRILL_CONFIG = $Config
    $env:N0DING_DRILL_CACHE_VOLUME = $Volume
    $env:N0DING_DRILL_PORT = [string]$script:drillPort
}

function Invoke-Compose {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Project,
        [Parameter(Mandatory = $true)]
        [string[]]$Arguments,
        [string]$OutputPath
    )

    Invoke-Docker -Arguments (
        @("compose", "--file", $composeFile, "--project-name", $Project) + $Arguments
    ) -OutputPath $OutputPath
}

function New-DrillVolume {
    param([Parameter(Mandatory = $true)][string]$Name)

    & docker volume inspect $Name *> $null
    if ($LASTEXITCODE -eq 0) {
        throw "Generated drill volume unexpectedly exists: $Name"
    }
    Invoke-Docker -Arguments @("volume", "create", $Name) -OutputPath (
        Join-Path $EvidenceRoot "volume-$Name.txt"
    )
    $createdVolumes.Add($Name)
}

function Wait-Health {
    param([Parameter(Mandatory = $true)][string]$BaseURL)

    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        try {
            $health = Invoke-RestMethod -Uri "$BaseURL/healthz" -TimeoutSec 2
            if ($health.status -eq "ok") {
                return
            }
        } catch {
        }
        Start-Sleep -Milliseconds 500
    }
    throw "n0ding did not become healthy at $BaseURL"
}

function Invoke-NpmClient {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Project,
        [Parameter(Mandatory = $true)]
        [string]$Label
    )

    $clientPath = Join-Path $EvidenceRoot "npm-$Label"
    New-Item -ItemType Directory -Path $clientPath | Out-Null
    Copy-Item -LiteralPath (Join-Path $npmFixture "package.json") -Destination $clientPath
    Copy-Item -LiteralPath (Join-Path $npmFixture "package-lock.json") -Destination $clientPath
    $lockfile = Join-Path $clientPath "package-lock.json"
    $hashBefore = (Get-FileHash -Algorithm SHA256 -LiteralPath $lockfile).Hash
    $network = "${Project}_default"
    $mount = "type=bind,src=$clientPath,dst=/workspace"

    Invoke-Docker -Arguments @(
        "run", "--rm",
        "--network", $network,
        "--mount", $mount,
        "--workdir", "/workspace",
        "node:24-alpine",
        "npm", "view", "n0ding-restore-fixture", "version",
        "--registry", "http://n0ding:8080/npm/",
        "--cache", "/tmp/npm-view-cache",
        "--prefer-online",
        "--loglevel", "warn"
    ) -OutputPath (Join-Path $EvidenceRoot "npm-$Label-view.log")

    Invoke-Docker -Arguments @(
        "run", "--rm",
        "--network", $network,
        "--mount", $mount,
        "--workdir", "/workspace",
        "node:24-alpine",
        "npm", "ci",
        "--ignore-scripts",
        "--no-audit",
        "--no-fund",
        "--registry", "http://n0ding:8080/npm/",
        "--cache", "/tmp/npm-ci-cache",
        "--prefer-online",
        "--loglevel", "warn"
    ) -OutputPath (Join-Path $EvidenceRoot "npm-$Label-ci.log")

    $installedModule = Join-Path $clientPath "node_modules/n0ding-restore-fixture/index.js"
    if (-not (Test-Path -LiteralPath $installedModule)) {
        throw "npm ci did not install the deterministic fixture for $Label"
    }
    $hashAfter = (Get-FileHash -Algorithm SHA256 -LiteralPath $lockfile).Hash
    if ($hashAfter -ne $hashBefore) {
        throw "npm ci changed the committed lockfile for $Label"
    }
    return $hashAfter
}

function Get-HeaderValue {
    param(
        [Parameter(Mandatory = $true)]
        [Net.Http.HttpResponseMessage]$Response,
        [Parameter(Mandatory = $true)]
        [string]$Name
    )

    $values = $null
    if ($Response.Headers.TryGetValues($Name, [ref]$values)) {
        return ($values -join ",")
    }
    if ($Response.Content.Headers.TryGetValues($Name, [ref]$values)) {
        return ($values -join ",")
    }
    return ""
}

function Invoke-DrillRequest {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Label,
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [string]$Token,
        [string]$Accept,
        [Parameter(Mandatory = $true)]
        [int]$ExpectedStatus,
        [Parameter(Mandatory = $true)]
        [string]$ExpectedCache,
        [Parameter(Mandatory = $true)]
        [string]$ExpectedBody,
        [string]$ExpectedAuthenticationInfo
    )

    $request = [Net.Http.HttpRequestMessage]::new(
        [Net.Http.HttpMethod]::Get,
        "$script:baseURL$Path"
    )
    try {
        if (-not [string]::IsNullOrWhiteSpace($Token)) {
            [void]$request.Headers.TryAddWithoutValidation("Authorization", "Bearer $Token")
        }
        if (-not [string]::IsNullOrWhiteSpace($Accept)) {
            [void]$request.Headers.TryAddWithoutValidation("Accept", $Accept)
        }
        $response = $script:httpClient.Send($request)
        try {
            $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            $cache = Get-HeaderValue -Response $response -Name "X-N0ding-Cache"
            $authenticationInfo = Get-HeaderValue -Response $response -Name "Authentication-Info"
            if ([int]$response.StatusCode -ne $ExpectedStatus) {
                throw "$Label returned status $([int]$response.StatusCode), want $ExpectedStatus"
            }
            if ($cache -ne $ExpectedCache) {
                throw "$Label returned cache result '$cache', want '$ExpectedCache'"
            }
            if ($body -ne $ExpectedBody) {
                throw "$Label returned an unexpected response body"
            }
            if (-not [string]::IsNullOrWhiteSpace($ExpectedAuthenticationInfo) -and
                $authenticationInfo -ne $ExpectedAuthenticationInfo) {
                throw "$Label did not receive the transient authentication response canary"
            }

            $record = [ordered]@{
                label                 = $Label
                status                = [int]$response.StatusCode
                cache                 = $cache
                docker_content_digest = Get-HeaderValue -Response $response -Name "Docker-Content-Digest"
                body_sha256           = [Convert]::ToHexString(
                    [Security.Cryptography.SHA256]::HashData(
                        [Text.Encoding]::UTF8.GetBytes($body)
                    )
                ).ToLowerInvariant()
                body                  = $body
            }
            $record | ConvertTo-Json |
                Set-Content -LiteralPath (Join-Path $EvidenceRoot "response-$Label.json")
            return [pscustomobject]$record
        } finally {
            $response.Dispose()
        }
    } finally {
        $request.Dispose()
    }
}

function Save-OperatorOutputs {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Project,
        [Parameter(Mandatory = $true)]
        [string]$Label
    )

    $statusPath = Join-Path $EvidenceRoot "status-$Label.json"
    $metricsPath = Join-Path $EvidenceRoot "metrics-$Label.txt"
    $statusText = $script:httpClient.GetStringAsync(
        "$script:baseURL/api/v1/status"
    ).GetAwaiter().GetResult()
    $metricsText = $script:httpClient.GetStringAsync(
        "$script:baseURL/metrics"
    ).GetAwaiter().GetResult()
    [IO.File]::WriteAllText($statusPath, $statusText)
    [IO.File]::WriteAllText($metricsPath, $metricsText)
    Invoke-Compose -Project $Project -Arguments @("logs", "--no-color") -OutputPath (
        Join-Path $EvidenceRoot "compose-$Label.log"
    )
    return $statusText | ConvertFrom-Json
}

function Get-RepositorySnapshot {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Status,
        [Parameter(Mandatory = $true)]
        [string]$Name
    )

    $snapshot = $Status.repositories | Where-Object { $_.name -eq $Name }
    if ($null -eq $snapshot) {
        throw "Status did not contain repository $Name"
    }
    return $snapshot
}

function Assert-CacheShape {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Status,
        [Parameter(Mandatory = $true)]
        [int]$NpmObjects,
        [Parameter(Mandatory = $true)]
        [int]$OCIObjects
    )

    $npm = Get-RepositorySnapshot -Status $Status -Name "npm"
    $oci = Get-RepositorySnapshot -Status $Status -Name "oci"
    if ($npm.cache_objects -ne $NpmObjects) {
        throw "npm cache objects = $($npm.cache_objects), want $NpmObjects"
    }
    if ($oci.cache_objects -ne $OCIObjects) {
        throw "OCI cache objects = $($oci.cache_objects), want $OCIObjects"
    }
}

function Stop-N0dingForBackup {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Project
    )

    Invoke-Compose -Project $Project -Arguments @("stop", "n0ding")
    $containerID = & docker compose --file $composeFile --project-name $Project `
        ps --all --quiet n0ding
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($containerID)) {
        throw "Could not resolve stopped n0ding container for $Project"
    }
    $running = & docker inspect --format "{{.State.Running}}" $containerID.Trim()
    if ($LASTEXITCODE -ne 0 -or $running.Trim() -ne "false") {
        throw "n0ding was still running when backup started"
    }
}

function New-BackupArchive {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Volume,
        [Parameter(Mandatory = $true)]
        [string]$Config
    )

    $archivePath = Join-Path $EvidenceRoot "n0ding-backup.tar"
    $timer = [Diagnostics.Stopwatch]::StartNew()
    Invoke-Docker -Arguments @(
        "run", "--rm",
        "--user", "0:0",
        "--entrypoint", "/bin/sh",
        "--volume", "${Volume}:/source:ro",
        "--mount", "type=bind,src=$Config,dst=/source-config/n0ding.toml,readonly",
        "--mount", "type=bind,src=$EvidenceRoot,dst=/backup",
        $n0dingImage,
        "-c",
        "set -eu; mkdir -p /staging/data /staging/config; cp -a /source/. /staging/data/; cp /source-config/n0ding.toml /staging/config/n0ding.toml; tar -cf /backup/n0ding-backup.tar -C /staging data config"
    )
    $timer.Stop()
    if (-not (Test-Path -LiteralPath $archivePath)) {
        throw "Backup archive was not created"
    }
    return [pscustomobject]@{
        Path       = $archivePath
        DurationMS = $timer.ElapsedMilliseconds
        SHA256     = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
    }
}

function Test-ArchiveMembers {
    param([Parameter(Mandatory = $true)][string]$ArchivePath)

    $contentsPath = Join-Path $EvidenceRoot "archive-contents.txt"
    Invoke-Docker -Arguments @(
        "run", "--rm",
        "--entrypoint", "/bin/tar",
        "--mount", "type=bind,src=$EvidenceRoot,dst=/backup,readonly",
        $n0dingImage,
        "-tf", "/backup/$(Split-Path -Leaf $ArchivePath)"
    ) -OutputPath $contentsPath
    $members = Get-Content -LiteralPath $contentsPath
    if ($members.Count -eq 0) {
        throw "Backup archive is empty"
    }
    foreach ($member in $members) {
        $normalized = $member.Replace("\", "/")
        if ($normalized.StartsWith("/") -or
            $normalized -match '(^|/)\.\.(/|$)' -or
            ($normalized -notmatch '^(data|config)(/|$)')) {
            throw "Unsafe or unexpected archive member: $member"
        }
    }
    if ($members -notcontains "config/n0ding.toml") {
        throw "Backup archive does not contain the n0ding config"
    }
}

function Restore-BackupArchive {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Volume,
        [switch]$CorruptNpmBody
    )

    $mutate = ""
    if ($CorruptNpmBody) {
        $mutate = 'body="$(find /restore/npm -type f -name ''*.body'' -print -quit)"; test -n "$body"; printf x > "$body";'
    }
    $restoreScript = 'set -eu; test -z "$(find /restore -mindepth 1 -print -quit)"; mkdir -p /staging; tar -xf /backup/n0ding-backup.tar -C /staging; test -d /staging/data; test -f /staging/config/n0ding.toml; cp -a /staging/data/. /restore/; cp /staging/config/n0ding.toml /backup/restored-config.toml; ' + $mutate
    $timer = [Diagnostics.Stopwatch]::StartNew()
    Invoke-Docker -Arguments @(
        "run", "--rm",
        "--user", "0:0",
        "--entrypoint", "/bin/sh",
        "--volume", "${Volume}:/restore",
        "--mount", "type=bind,src=$EvidenceRoot,dst=/backup",
        $n0dingImage,
        "-c",
        $restoreScript
    )
    $timer.Stop()
    return $timer.ElapsedMilliseconds
}

function Export-Volume {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Volume,
        [Parameter(Mandatory = $true)]
        [string]$DirectoryName
    )

    Invoke-Docker -Arguments @(
        "run", "--rm",
        "--user", "0:0",
        "--entrypoint", "/bin/sh",
        "--volume", "${Volume}:/source:ro",
        "--mount", "type=bind,src=$EvidenceRoot,dst=/evidence",
        $n0dingImage,
        "-c",
        "set -eu; mkdir -p /evidence/$DirectoryName; cp -a /source/. /evidence/$DirectoryName/"
    )
}

function Get-FreeTcpPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    } finally {
        $listener.Stop()
    }
}

$script:drillPort = Get-FreeTcpPort
$script:baseURL = "http://127.0.0.1:$script:drillPort"
$script:httpClient = [Net.Http.HttpClient]::new()
$restoredConfig = Join-Path $EvidenceRoot "restored-config.toml"

try {
    foreach ($entry in $canaryValues.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value)
    }

    Invoke-Docker -Arguments @("version") -OutputPath (
        Join-Path $EvidenceRoot "docker-version.txt"
    )
    Invoke-Docker -Arguments @("compose", "version") -OutputPath (
        Join-Path $EvidenceRoot "compose-version.txt"
    )

    New-DrillVolume -Name $sourceVolume
    Set-StackEnvironment -Volume $sourceVolume -Config $sourceConfig
    Invoke-Compose -Project $sourceProject -Arguments @("config", "--quiet")
    Invoke-Compose -Project $sourceProject -Arguments @(
        "up", "--build", "--detach", "--wait", "--wait-timeout", "180"
    )
    Wait-Health -BaseURL $script:baseURL

    $lockfileHashBefore = Invoke-NpmClient -Project $sourceProject -Label "before"
    Invoke-DrillRequest -Label "npm-private-a-before" `
        -Path "/npm/private-package?access_token=$($canaryValues.N0DING_BACKUP_QUERY_CANARY)" `
        -Token $canaryValues.N0DING_BACKUP_NPM_TOKEN_A `
        -ExpectedStatus 200 -ExpectedCache "MISS" `
        -ExpectedBody "private npm body for identity A" `
        -ExpectedAuthenticationInfo "nextnonce=`"$($canaryValues.N0DING_BACKUP_RESPONSE_CANARY)`"" | Out-Null
    Invoke-DrillRequest -Label "npm-private-b-before" `
        -Path "/npm/private-package" `
        -Token $canaryValues.N0DING_BACKUP_NPM_TOKEN_B `
        -ExpectedStatus 200 -ExpectedCache "MISS" `
        -ExpectedBody "private npm body for identity B" | Out-Null
    Invoke-DrillRequest -Label "npm-denied-before" `
        -Path "/npm/private-package" `
        -Token $canaryValues.N0DING_BACKUP_NPM_TOKEN_DENIED `
        -ExpectedStatus 403 -ExpectedCache "MISS" -ExpectedBody "denied`n" | Out-Null

    $ociPath = "/v2/private/restore/manifests/latest"
    $ociAccept = "application/vnd.oci.image.manifest.v1+json"
    $ociBefore = Invoke-DrillRequest -Label "oci-a-before" -Path $ociPath `
        -Token $canaryValues.N0DING_BACKUP_OCI_TOKEN_A -Accept $ociAccept `
        -ExpectedStatus 200 -ExpectedCache "MISS" `
        -ExpectedBody '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[]}'
    Invoke-DrillRequest -Label "oci-b-before" -Path $ociPath `
        -Token $canaryValues.N0DING_BACKUP_OCI_TOKEN_B -Accept $ociAccept `
        -ExpectedStatus 200 -ExpectedCache "HIT" -ExpectedBody $ociBefore.body | Out-Null
    Invoke-DrillRequest -Label "oci-denied-before" -Path $ociPath `
        -Token $canaryValues.N0DING_BACKUP_OCI_TOKEN_DENIED -Accept $ociAccept `
        -ExpectedStatus 403 -ExpectedCache "MISS" -ExpectedBody "denied`n" | Out-Null
    if ($ociBefore.docker_content_digest -ne "sha256:$($ociBefore.body_sha256)") {
        throw "OCI fixture digest did not match its body before backup"
    }

    $sourceStatus = Save-OperatorOutputs -Project $sourceProject -Label "before"
    Assert-CacheShape -Status $sourceStatus -NpmObjects 2 -OCIObjects 1
    $sourceNpm = Get-RepositorySnapshot -Status $sourceStatus -Name "npm"
    $sourceOCI = Get-RepositorySnapshot -Status $sourceStatus -Name "oci"

    Stop-N0dingForBackup -Project $sourceProject
    Invoke-Compose -Project $sourceProject -Arguments @("logs", "--no-color") -OutputPath (
        Join-Path $EvidenceRoot "compose-before-stopped.log"
    )
    $backup = New-BackupArchive -Volume $sourceVolume -Config $sourceConfig
    Test-ArchiveMembers -ArchivePath $backup.Path

    New-DrillVolume -Name $restoredVolume
    $restoreDurationMS = Restore-BackupArchive -Volume $restoredVolume
    if (-not (Test-Path -LiteralPath $restoredConfig)) {
        throw "Restored configuration was not extracted"
    }
    $projects[1].Config = $restoredConfig

    Set-StackEnvironment -Volume $restoredVolume -Config $restoredConfig
    Invoke-Compose -Project $restoredProject -Arguments @(
        "up", "--detach", "--wait", "--wait-timeout", "180", "--no-build"
    )
    Wait-Health -BaseURL $script:baseURL

    $lockfileHashAfter = Invoke-NpmClient -Project $restoredProject -Label "after"
    if ($lockfileHashAfter -ne $lockfileHashBefore) {
        throw "npm lockfile hash changed across backup/restore"
    }
    Invoke-DrillRequest -Label "npm-private-a-after" -Path "/npm/private-package" `
        -Token $canaryValues.N0DING_BACKUP_NPM_TOKEN_A `
        -ExpectedStatus 200 -ExpectedCache "MISS" `
        -ExpectedBody "private npm body for identity A" | Out-Null
    Invoke-DrillRequest -Label "npm-private-b-after" -Path "/npm/private-package" `
        -Token $canaryValues.N0DING_BACKUP_NPM_TOKEN_B `
        -ExpectedStatus 200 -ExpectedCache "MISS" `
        -ExpectedBody "private npm body for identity B" | Out-Null
    Invoke-DrillRequest -Label "npm-denied-after" -Path "/npm/private-package" `
        -Token $canaryValues.N0DING_BACKUP_NPM_TOKEN_DENIED `
        -ExpectedStatus 403 -ExpectedCache "MISS" -ExpectedBody "denied`n" | Out-Null
    Invoke-DrillRequest -Label "oci-denied-after" -Path $ociPath `
        -Token $canaryValues.N0DING_BACKUP_OCI_TOKEN_DENIED -Accept $ociAccept `
        -ExpectedStatus 403 -ExpectedCache "MISS" -ExpectedBody "denied`n" | Out-Null
    $ociAfter = Invoke-DrillRequest -Label "oci-b-after" -Path $ociPath `
        -Token $canaryValues.N0DING_BACKUP_OCI_TOKEN_B -Accept $ociAccept `
        -ExpectedStatus 200 -ExpectedCache "HIT" -ExpectedBody $ociBefore.body
    Invoke-DrillRequest -Label "oci-a-after" -Path $ociPath `
        -Token $canaryValues.N0DING_BACKUP_OCI_TOKEN_A -Accept $ociAccept `
        -ExpectedStatus 200 -ExpectedCache "HIT" -ExpectedBody $ociBefore.body | Out-Null
    if ($ociAfter.docker_content_digest -ne $ociBefore.docker_content_digest) {
        throw "OCI digest changed across backup/restore"
    }

    $restoredStatus = Save-OperatorOutputs -Project $restoredProject -Label "after"
    Assert-CacheShape -Status $restoredStatus -NpmObjects 2 -OCIObjects 1
    $restoredNpm = Get-RepositorySnapshot -Status $restoredStatus -Name "npm"
    $restoredOCI = Get-RepositorySnapshot -Status $restoredStatus -Name "oci"
    if ($restoredNpm.cache_hits -lt 2 -or $restoredOCI.cache_hits -lt 2) {
        throw "Restored npm/OCI objects were not served as cache hits"
    }
    if ($restoredNpm.storage_bytes -ne $sourceNpm.storage_bytes -or
        $restoredOCI.storage_bytes -ne $sourceOCI.storage_bytes) {
        throw "Restored repository byte counts differ from the source backup"
    }

    Stop-N0dingForBackup -Project $restoredProject
    Invoke-Compose -Project $restoredProject -Arguments @("logs", "--no-color") -OutputPath (
        Join-Path $EvidenceRoot "compose-after-stopped.log"
    )
    Export-Volume -Volume $restoredVolume -DirectoryName "restored-state"

    Set-StackEnvironment -Volume $sourceVolume -Config $sourceConfig
    Invoke-Compose -Project $sourceProject -Arguments @("start", "n0ding")
    Wait-Health -BaseURL $script:baseURL
    Invoke-DrillRequest -Label "oci-a-rollback" -Path $ociPath `
        -Token $canaryValues.N0DING_BACKUP_OCI_TOKEN_A -Accept $ociAccept `
        -ExpectedStatus 200 -ExpectedCache "HIT" -ExpectedBody $ociBefore.body | Out-Null
    $rollbackStatus = Save-OperatorOutputs -Project $sourceProject -Label "rollback"
    Assert-CacheShape -Status $rollbackStatus -NpmObjects 2 -OCIObjects 1
    Stop-N0dingForBackup -Project $sourceProject

    New-DrillVolume -Name $corruptVolume
    $null = Restore-BackupArchive -Volume $corruptVolume -CorruptNpmBody
    $projects[2].Config = $restoredConfig
    Set-StackEnvironment -Volume $corruptVolume -Config $restoredConfig
    Invoke-Compose -Project $corruptProject -Arguments @(
        "up", "--detach", "--wait", "--wait-timeout", "180", "--no-build"
    )
    Wait-Health -BaseURL $script:baseURL
    $null = Invoke-NpmClient -Project $corruptProject -Label "corrupt"
    $corruptStatus = Save-OperatorOutputs -Project $corruptProject -Label "corrupt"
    Assert-CacheShape -Status $corruptStatus -NpmObjects 2 -OCIObjects 1
    $corruptLog = Get-Content -LiteralPath (
        Join-Path $EvidenceRoot "compose-corrupt.log"
    ) -Raw
    if ($corruptLog -notmatch "cache body size mismatch") {
        throw "Corrupt restore did not produce the expected cache lookup warning"
    }
    Stop-N0dingForBackup -Project $corruptProject

    $result = [ordered]@{
        status                    = "pending_canary_scan"
        source_commit             = (& git -C $repositoryRoot rev-parse HEAD).Trim()
        backup_sha256             = $backup.SHA256
        backup_duration_ms        = $backup.DurationMS
        restore_duration_ms       = $restoreDurationMS
        npm_lockfile_sha256       = $lockfileHashAfter.ToLowerInvariant()
        npm_objects               = $restoredNpm.cache_objects
        npm_storage_bytes         = $restoredNpm.storage_bytes
        npm_restored_hits         = $restoredNpm.cache_hits
        oci_digest                = $ociAfter.docker_content_digest
        oci_objects               = $restoredOCI.cache_objects
        oci_storage_bytes         = $restoredOCI.storage_bytes
        oci_restored_hits         = $restoredOCI.cache_hits
        identity_safety_after     = "pass"
        rollback                  = "pass"
        corrupt_object_refetch    = "pass"
        credential_canary_scan    = "pending"
    }
    $resultPath = Join-Path $EvidenceRoot "result.json"
    $result | ConvertTo-Json | Set-Content -LiteralPath $resultPath

    & $canaryScanner -LiteralPath $EvidenceRoot -CanaryEnv @($canaryValues.Keys)
    if (-not $?) {
        throw "Credential canary scan failed"
    }
    $result["status"] = "pass"
    $result["credential_canary_scan"] = "pass"
    $result | ConvertTo-Json | Set-Content -LiteralPath $resultPath

    Write-Output "Stopped Compose backup/restore drill passed."
    Write-Output "Evidence: $EvidenceRoot"
    $result | Format-List
} finally {
    $script:httpClient.Dispose()

    foreach ($project in $projects) {
        $config = $project.Config
        if ([string]::IsNullOrWhiteSpace($config)) {
            $config = $sourceConfig
        }
        Set-StackEnvironment -Volume $project.Volume -Config $config
        & docker compose --file $composeFile --project-name $project.Name `
            down --remove-orphans *> $null
    }
    foreach ($volume in $createdVolumes) {
        & docker volume rm $volume *> $null
    }
    & docker image rm $n0dingImage $fixtureImage *> $null

    foreach ($name in $managedEnvironment) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name])
    }
}

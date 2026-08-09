[CmdletBinding()]
param(
    [ValidateSet("Smoke", "SevenDay")]
    [string]$Mode = "Smoke",

    [TimeSpan]$Duration = [TimeSpan]::Zero,

    [TimeSpan]$SampleInterval = [TimeSpan]::Zero,

    [TimeSpan]$RetentionAge = [TimeSpan]::Zero,

    [TimeSpan]$GCInterval = [TimeSpan]::Zero,

    [TimeSpan]$RestartInterval = [TimeSpan]::Zero,

    [int]$Workers = 0,

    [string]$EvidenceRoot
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if ($Mode -eq "Smoke") {
    if ($Duration -eq [TimeSpan]::Zero) {
        $Duration = [TimeSpan]::FromSeconds(45)
    }
    if ($SampleInterval -eq [TimeSpan]::Zero) {
        $SampleInterval = [TimeSpan]::FromSeconds(2)
    }
    if ($RetentionAge -eq [TimeSpan]::Zero) {
        $RetentionAge = [TimeSpan]::FromSeconds(15)
    }
    if ($GCInterval -eq [TimeSpan]::Zero) {
        $GCInterval = [TimeSpan]::FromSeconds(1)
    }
    if ($Workers -eq 0) {
        $Workers = 6
    }
} else {
    if ($Duration -eq [TimeSpan]::Zero) {
        $Duration = [TimeSpan]::FromDays(7)
    }
    if ($SampleInterval -eq [TimeSpan]::Zero) {
        $SampleInterval = [TimeSpan]::FromMinutes(5)
    }
    if ($RetentionAge -eq [TimeSpan]::Zero) {
        $RetentionAge = [TimeSpan]::FromMinutes(10)
    }
    if ($GCInterval -eq [TimeSpan]::Zero) {
        $GCInterval = [TimeSpan]::FromMinutes(1)
    }
    if ($RestartInterval -eq [TimeSpan]::Zero) {
        $RestartInterval = [TimeSpan]::FromHours(6)
    }
    if ($Workers -eq 0) {
        $Workers = 8
    }
}

$staleTempAge = [TimeSpan]::FromSeconds(2)
$forcedExpiryWait = $RetentionAge + $GCInterval + $GCInterval
if ($Duration -lt ($forcedExpiryWait + [TimeSpan]::FromSeconds(12))) {
    throw "Duration must leave at least 12 seconds after the forced-expiry window ($forcedExpiryWait)."
}
if ($SampleInterval -le [TimeSpan]::Zero) {
    throw "SampleInterval must be positive."
}
if ($RetentionAge -le [TimeSpan]::Zero -or $GCInterval -le [TimeSpan]::Zero) {
    throw "RetentionAge and GCInterval must be positive."
}
if ($Workers -lt 2) {
    throw "Workers must be at least 2."
}
if ($RestartInterval -lt [TimeSpan]::Zero) {
    throw "RestartInterval cannot be negative."
}

$repositoryRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$composeFile = Join-Path $repositoryRoot "testdata/soak/compose.yaml"
$soakConfig = Join-Path $repositoryRoot "testdata/soak/n0ding.toml"
$canaryScanner = Join-Path $repositoryRoot "tools/private-canary-scan.ps1"
$suffix = [Guid]::NewGuid().ToString("N").Substring(0, 10)

if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $repositoryRoot ".tmp/retention-soak/$suffix"
} else {
    $EvidenceRoot = [IO.Path]::GetFullPath($EvidenceRoot)
}
if (Test-Path -LiteralPath $EvidenceRoot) {
    throw "Evidence path already exists; refusing to overwrite it: $EvidenceRoot"
}
New-Item -ItemType Directory -Path $EvidenceRoot | Out-Null
New-Item -ItemType Directory -Path (Join-Path $EvidenceRoot "workload") | Out-Null
New-Item -ItemType Directory -Path (Join-Path $EvidenceRoot "snapshots") | Out-Null

$project = "n0ding-soak-$suffix"
$cacheVolume = "n0ding-soak-$suffix-data"
$n0dingImage = "n0ding:retention-soak-$suffix"
$fixtureImage = "n0ding-fixture:retention-soak-$suffix"
$volumeCreated = $false
$stackStarted = $false
$script:snapshots = [Collections.Generic.List[object]]::new()
$script:epochEnds = [Collections.Generic.List[object]]::new()
$script:clientHits = 0L
$script:clientMisses = 0L
$script:deniedChecks = 0L
$script:cycles = 0
$script:restartCount = 0
$script:maxDiskKiB = 0L
$script:lastCycle = "seed"

$canaryValues = [ordered]@{
    N0DING_SOAK_NPM_TOKEN_A        = "n0ding-soak-npm-token-a-canary"
    N0DING_SOAK_NPM_TOKEN_B        = "n0ding-soak-npm-token-b-canary"
    N0DING_SOAK_NPM_TOKEN_DENIED   = "n0ding-soak-npm-denied-canary"
    N0DING_SOAK_OCI_TOKEN_A        = "n0ding-soak-oci-token-a-canary"
    N0DING_SOAK_OCI_TOKEN_B        = "n0ding-soak-oci-token-b-canary"
    N0DING_SOAK_OCI_TOKEN_DENIED   = "n0ding-soak-oci-denied-canary"
    N0DING_SOAK_QUERY_CANARY       = "n0ding-soak-query-canary"
    N0DING_SOAK_RESPONSE_CANARY    = "n0ding-soak-response-canary"
}
$managedEnvironment = @(
    "N0DING_SOAK_IMAGE",
    "N0DING_SOAK_FIXTURE_IMAGE",
    "N0DING_SOAK_CONFIG",
    "N0DING_SOAK_CACHE_VOLUME",
    "N0DING_SOAK_PORT",
    "N0DING_SOAK_FIXTURE_PORT",
    "N0DING_SOAK_MAX_AGE",
    "N0DING_SOAK_GC_INTERVAL",
    "N0DING_SOAK_STALE_TEMP_AGE"
) + @($canaryValues.Keys)
$previousEnvironment = @{}
foreach ($name in $managedEnvironment) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name)
}

function Format-GoDuration {
    param([Parameter(Mandatory = $true)][TimeSpan]$Value)

    $seconds = [Math]::Ceiling($Value.TotalSeconds)
    return "${seconds}s"
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
        $summary = ($Arguments | Select-Object -First 4) -join " "
        throw "Docker command failed ($LASTEXITCODE): docker $summary"
    }
}

function Invoke-Compose {
    param(
        [Parameter(Mandatory = $true)]
        [string[]]$Arguments,
        [string]$OutputPath
    )

    Invoke-Docker -Arguments (
        @("compose", "--file", $composeFile, "--project-name", $project) + $Arguments
    ) -OutputPath $OutputPath
}

function Get-FreeTcpPorts {
    $first = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $second = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    try {
        $first.Start()
        $second.Start()
        return @(
            ([Net.IPEndPoint]$first.LocalEndpoint).Port,
            ([Net.IPEndPoint]$second.LocalEndpoint).Port
        )
    } finally {
        $first.Stop()
        $second.Stop()
    }
}

function Wait-Health {
    for ($attempt = 0; $attempt -lt 90; $attempt++) {
        try {
            $health = Invoke-RestMethod -Uri "$script:n0dingURL/healthz" -TimeoutSec 2
            $fixtureHealth = Invoke-RestMethod -Uri "$script:fixtureURL/healthz" -TimeoutSec 2
            if ($health.status -eq "ok" -and $fixtureHealth.status -eq "ok") {
                return
            }
        } catch {
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Soak stack did not become healthy."
}

function Get-FixtureStats {
    return Invoke-RestMethod -Uri "$script:fixtureURL/control/stats" -TimeoutSec 5
}

function Get-VolumeUsage {
    $command = 'set -eu; kib="$(du -sk /data | awk ''{print $1}'')"; files="$(find /data -type f | wc -l | tr -d '' '')"; temps="$(find /data -type f \( -name ''.body-*'' -o -name ''.metadata-*'' \) | wc -l | tr -d '' '')"; printf ''{"disk_kib":%s,"files":%s,"temp_files":%s}\n'' "$kib" "$files" "$temps"'
    $output = & docker run --rm `
        --user "0:0" `
        --entrypoint /bin/sh `
        --volume "${cacheVolume}:/data:ro" `
        $n0dingImage -c $command
    if ($LASTEXITCODE -ne 0) {
        throw "Could not inspect soak cache volume."
    }
    return ($output -join "`n") | ConvertFrom-Json
}

function Get-Repository {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Status,
        [Parameter(Mandatory = $true)]
        [string]$Name
    )

    $repository = $Status.repositories | Where-Object { $_.name -eq $Name }
    if ($null -eq $repository) {
        throw "Status does not contain repository $Name."
    }
    return $repository
}

function Get-MetricValue {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Metrics,
        [Parameter(Mandatory = $true)]
        [string]$Metric,
        [Parameter(Mandatory = $true)]
        [string]$Repository,
        [Parameter(Mandatory = $true)]
        [string]$Type
    )

    $pattern = "(?m)^n0ding_repository_$([regex]::Escape($Metric))\{repository=`"$([regex]::Escape($Repository))`",type=`"$([regex]::Escape($Type))`"\} ([0-9.]+)$"
    $match = [regex]::Match($Metrics, $pattern)
    if (-not $match.Success) {
        throw "Metric $Metric for repository $Repository is missing."
    }
    return [double]::Parse(
        $match.Groups[1].Value,
        [Globalization.CultureInfo]::InvariantCulture
    )
}

function Assert-StatusMetrics {
    param(
        [Parameter(Mandatory = $true)]
        [object]$Status,
        [Parameter(Mandatory = $true)]
        [string]$Metrics
    )

    $fields = [ordered]@{
        requests_total       = "requests"
        cache_hits_total     = "cache_hits"
        cache_misses_total   = "cache_misses"
        errors_total         = "errors"
        range_requests_total = "range_requests"
        storage_bytes        = "storage_bytes"
        cache_objects        = "cache_objects"
    }
    foreach ($repository in $Status.repositories) {
        foreach ($entry in $fields.GetEnumerator()) {
            $metricValue = Get-MetricValue -Metrics $Metrics -Metric $entry.Key `
                -Repository $repository.name -Type $repository.type
            $statusValue = [double]$repository.($entry.Value)
            if ($metricValue -ne $statusValue) {
                throw "Status/metrics mismatch for $($repository.name).$($entry.Value): $statusValue != $metricValue"
            }
        }
    }
}

function Save-Snapshot {
    param([Parameter(Mandatory = $true)][string]$Label)

    $safeLabel = $Label -replace '[^A-Za-z0-9_.-]', '-'
    $directory = Join-Path $EvidenceRoot "snapshots"
    for ($attempt = 0; $attempt -lt 5; $attempt++) {
        $statusText = $script:httpClient.GetStringAsync(
            "$script:n0dingURL/api/v1/status"
        ).GetAwaiter().GetResult()
        $metricsText = $script:httpClient.GetStringAsync(
            "$script:n0dingURL/metrics"
        ).GetAwaiter().GetResult()
        $status = $statusText | ConvertFrom-Json
        try {
            Assert-StatusMetrics -Status $status -Metrics $metricsText
            break
        } catch {
            if ($attempt -eq 4) {
                throw
            }
            Start-Sleep -Milliseconds 100
        }
    }
    $usage = Get-VolumeUsage
    $fixtureStats = Get-FixtureStats
    if ([int64]$usage.disk_kib -gt $script:maxDiskKiB) {
        $script:maxDiskKiB = [int64]$usage.disk_kib
    }

    [IO.File]::WriteAllText(
        (Join-Path $directory "status-$safeLabel.json"),
        $statusText
    )
    [IO.File]::WriteAllText(
        (Join-Path $directory "metrics-$safeLabel.txt"),
        $metricsText
    )
    $usage | ConvertTo-Json |
        Set-Content -LiteralPath (Join-Path $directory "volume-$safeLabel.json")
    $fixtureStats | ConvertTo-Json |
        Set-Content -LiteralPath (Join-Path $directory "fixture-$safeLabel.json")

    $snapshot = [pscustomobject]@{
        label         = $safeLabel
        recorded_at   = [DateTimeOffset]::UtcNow.ToString("o")
        status        = $status
        volume        = $usage
        fixture_stats = $fixtureStats
    }
    $script:snapshots.Add($snapshot)
    return $snapshot
}

function Add-ClientMeasurements {
    param([Parameter(Mandatory = $true)][object]$Result)

    foreach ($name in @("npm_metadata", "npm_tarball", "oci_manifest", "oci_blob")) {
        $summary = $Result.$name
        $script:clientHits += [int64]$summary.hits
        $script:clientMisses += [int64]$summary.misses
    }
    $script:clientMisses += 4
    $script:deniedChecks += 2
}

function Invoke-Workload {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Cycle,
        [ValidateSet("cold", "warm")]
        [string]$Expectation = "cold"
    )

    $safeCycle = $Cycle -replace '[^A-Za-z0-9_.-]', '-'
    $outputPath = Join-Path $EvidenceRoot "workload/cycle-$safeCycle-$Expectation.json"
    Invoke-Compose -Arguments @(
        "exec", "--no-TTY", "fixture",
        "/usr/local/bin/n0ding-soak-client",
        "-mode", "workload",
        "-n0ding-url", "http://n0ding:8080",
        "-fixture-url", "http://localhost:9090",
        "-cycle", $safeCycle,
        "-workers", [string]$Workers,
        "-expect", $Expectation
    ) -OutputPath $outputPath
    $result = Get-Content -LiteralPath $outputPath -Raw | ConvertFrom-Json
    if ($result.status -ne "pass" -or $result.identity_safety -ne "pass") {
        throw "Workload cycle $safeCycle did not pass."
    }
    Add-ClientMeasurements -Result $result
    $script:cycles++
    $script:lastCycle = $safeCycle
    return $result
}

function Wait-SlowInactive {
    for ($attempt = 0; $attempt -lt 120; $attempt++) {
        if ((Get-FixtureStats).slow_active -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Slow fixture response did not become inactive."
}

function Wait-NoTemporaryFiles {
    for ($attempt = 0; $attempt -lt 120; $attempt++) {
        if ((Get-VolumeUsage).temp_files -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Temporary cache files did not disappear."
}

function Invoke-ClientAbort {
    param([Parameter(Mandatory = $true)][string]$Label)

    $before = Save-Snapshot -Label "$Label-before"
    $outputPath = Join-Path $EvidenceRoot "workload/abort-$Label.json"
    Invoke-Compose -Arguments @(
        "exec", "--no-TTY", "fixture",
        "/usr/local/bin/n0ding-soak-client",
        "-mode", "abort",
        "-n0ding-url", "http://n0ding:8080",
        "-fixture-url", "http://localhost:9090",
        "-cycle", $Label,
        "-workers", [string]$Workers
    ) -OutputPath $outputPath
    $abortResult = Get-Content -LiteralPath $outputPath -Raw | ConvertFrom-Json
    if ($abortResult.status -ne "pass" -or
        $abortResult.http_status -ne 200 -or
        $abortResult.cache -ne "MISS") {
        throw "Controlled client abort did not reach a cacheable OCI miss."
    }
    Wait-SlowInactive
    Wait-NoTemporaryFiles
    $after = Save-Snapshot -Label "$Label-after"
    foreach ($name in @("npm", "oci")) {
        $beforeRepository = Get-Repository -Status $before.status -Name $name
        $afterRepository = Get-Repository -Status $after.status -Name $name
        if ($beforeRepository.cache_objects -ne $afterRepository.cache_objects) {
            throw "Client abort changed complete object count for $name."
        }
    }
}

function Start-HeldDownload {
    param([Parameter(Mandatory = $true)][string]$Label)

    Invoke-Compose -Arguments @(
        "exec", "--detach", "fixture",
        "/usr/local/bin/n0ding-soak-client",
        "-mode", "hold",
        "-n0ding-url", "http://n0ding:8080",
        "-fixture-url", "http://localhost:9090",
        "-cycle", $Label,
        "-workers", [string]$Workers
    )
    for ($attempt = 0; $attempt -lt 120; $attempt++) {
        $stats = Get-FixtureStats
        $usage = Get-VolumeUsage
        if ($stats.slow_active -gt 0 -and $usage.temp_files -gt 0) {
            return
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Could not establish an active temporary cache write before restart."
}

function Invoke-ControlledRestart {
    param([Parameter(Mandatory = $true)][string]$Label)

    Start-HeldDownload -Label $Label
    $before = Save-Snapshot -Label "$Label-pre-kill"
    $script:epochEnds.Add($before.status)

    Invoke-Compose -Arguments @("kill", "--signal", "SIGKILL", "n0ding")
    Wait-SlowInactive
    Start-Sleep -Milliseconds ([int]($staleTempAge.TotalMilliseconds + 750))
    Invoke-Compose -Arguments @("start", "n0ding")
    Wait-Health
    Wait-NoTemporaryFiles

    $null = Invoke-Workload -Cycle $script:lastCycle -Expectation "warm"
    $null = Save-Snapshot -Label "$Label-post-start"
    $script:restartCount++
}

function Wait-ForcedExpiry {
    param([Parameter(Mandatory = $true)][DateTimeOffset]$Deadline)

    $waitUntil = [DateTimeOffset]::UtcNow + $forcedExpiryWait
    $sample = 0
    while ([DateTimeOffset]::UtcNow -lt $waitUntil) {
        if ([DateTimeOffset]::UtcNow -ge $Deadline) {
            throw "Requested duration ended before forced expiry completed."
        }
        $remaining = $waitUntil - [DateTimeOffset]::UtcNow
        $sleepSeconds = [Math]::Min(5.0, [Math]::Max(0.1, $remaining.TotalSeconds))
        Start-Sleep -Milliseconds ([int]($sleepSeconds * 1000))
        if ($Mode -eq "SevenDay" -and (++$sample % 12) -eq 0) {
            $null = Save-Snapshot -Label "forced-expiry-wait-$sample"
        }
    }
}

function Write-Progress {
    param(
        [Parameter(Mandatory = $true)]
        [DateTimeOffset]$StartedAt,
        [Parameter(Mandatory = $true)]
        [DateTimeOffset]$Deadline
    )

    $progress = [ordered]@{
        status                     = "running"
        mode                       = $Mode
        source_commit              = (& git -C $repositoryRoot rev-parse HEAD).Trim()
        started_at                 = $StartedAt.ToString("o")
        requested_duration_seconds = [int64]$Duration.TotalSeconds
        deadline                   = $Deadline.ToString("o")
        updated_at                 = [DateTimeOffset]::UtcNow.ToString("o")
        cycles                     = $script:cycles
        restarts                   = $script:restartCount
        client_cache_hits          = $script:clientHits
        client_cache_misses        = $script:clientMisses
        denied_identity_checks     = $script:deniedChecks
        max_disk_kib               = $script:maxDiskKiB
        artifact_path              = $EvidenceRoot
    }
    $progress | ConvertTo-Json |
        Set-Content -LiteralPath (Join-Path $EvidenceRoot "progress.json")
}

function Save-CacheSnapshot {
    Invoke-Compose -Arguments @("stop", "n0ding")
    $script:stackStarted = $false
    Invoke-Compose -Arguments @("logs", "--no-color") -OutputPath (
        Join-Path $EvidenceRoot "compose.log"
    )

    $snapshotCommand = 'set -eu; mkdir -p /evidence/cache-state /staging/cache /staging/config; cp -a /data/. /evidence/cache-state/; chmod -R a+rX /evidence/cache-state; cp -a /data/. /staging/cache/; cp /config/n0ding.toml /staging/config/n0ding.toml; tar -cf /evidence/cache-snapshot.tar -C /staging cache config; chmod a+r /evidence/cache-snapshot.tar'
    Invoke-Docker -Arguments @(
        "run", "--rm",
        "--user", "0:0",
        "--entrypoint", "/bin/sh",
        "--volume", "${cacheVolume}:/data:ro",
        "--mount", "type=bind,src=$soakConfig,dst=/config/n0ding.toml,readonly",
        "--mount", "type=bind,src=$EvidenceRoot,dst=/evidence",
        $n0dingImage,
        "-c", $snapshotCommand
    )
}

function Test-ExportedCache {
    $cacheRoot = Join-Path $EvidenceRoot "cache-state"
    $temporary = @(Get-ChildItem -LiteralPath $cacheRoot -File -Recurse |
        Where-Object {
            $_.Name.StartsWith(".body-") -or $_.Name.StartsWith(".metadata-")
        })
    if ($temporary.Count -ne 0) {
        throw "Exported cache contains $($temporary.Count) temporary file(s)."
    }

    $metadataFiles = @(Get-ChildItem -LiteralPath $cacheRoot -Filter "*.json" -File -Recurse)
    $bodyFiles = @(Get-ChildItem -LiteralPath $cacheRoot -File -Recurse |
        Where-Object { $_.Name -match '\.body(?:\.|$)' })
    $referencedBodies = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::OrdinalIgnoreCase
    )
    foreach ($metadataFile in $metadataFiles) {
        $metadata = Get-Content -LiteralPath $metadataFile.FullName -Raw | ConvertFrom-Json
        $bodyFileProperty = $metadata.PSObject.Properties["body_file"]
        if ($null -ne $bodyFileProperty -and
            -not [string]::IsNullOrWhiteSpace([string]$bodyFileProperty.Value)) {
            $bodyName = [string]$bodyFileProperty.Value
            if ([IO.Path]::GetFileName($bodyName) -ne $bodyName) {
                throw "Unsafe body_file in cache metadata: $($metadataFile.Name)"
            }
            $bodyPath = Join-Path $metadataFile.DirectoryName $bodyName
        } else {
            $bodyPath = [IO.Path]::ChangeExtension($metadataFile.FullName, ".body")
        }
        if (-not (Test-Path -LiteralPath $bodyPath -PathType Leaf)) {
            throw "Complete metadata has no body: $($metadataFile.Name)"
        }
        $null = $referencedBodies.Add([IO.Path]::GetFullPath($bodyPath))
        $body = Get-Item -LiteralPath $bodyPath
        if ($body.Length -ne [int64]$metadata.content_bytes) {
            throw "Cache body size mismatch in exported state: $($metadataFile.Name)"
        }
        $digestProperty = $metadata.PSObject.Properties["content_digest"]
        if ($null -ne $digestProperty -and
            -not [string]::IsNullOrWhiteSpace([string]$digestProperty.Value)) {
            $actual = "sha256:" + (
                Get-FileHash -Algorithm SHA256 -LiteralPath $bodyPath
            ).Hash.ToLowerInvariant()
            if ($actual -ne [string]$digestProperty.Value) {
                throw "OCI digest mismatch in exported state: $($metadataFile.Name)"
            }
        }
    }
    foreach ($bodyFile in $bodyFiles) {
        if (-not $referencedBodies.Contains([IO.Path]::GetFullPath($bodyFile.FullName))) {
            throw "Exported cache contains an orphan body: $($bodyFile.Name)"
        }
    }
    return [pscustomobject]@{
        objects = $metadataFiles.Count
        bodies  = $bodyFiles.Count
        temps   = $temporary.Count
    }
}

function Get-AggregateServerCounters {
    param([Parameter(Mandatory = $true)][object]$FinalStatus)

    $statuses = @($script:epochEnds) + @($FinalStatus)
    $result = [ordered]@{}
    foreach ($name in @("npm", "oci")) {
        $aggregate = [ordered]@{
            requests       = 0L
            cache_hits     = 0L
            cache_misses   = 0L
            errors         = 0L
            range_requests = 0L
        }
        foreach ($status in $statuses) {
            $repository = Get-Repository -Status $status -Name $name
            foreach ($field in @(
                "requests",
                "cache_hits",
                "cache_misses",
                "errors",
                "range_requests"
            )) {
                $aggregate[$field] += [int64]$repository.$field
            }
        }
        $result[$name] = $aggregate
    }
    return $result
}

$ports = Get-FreeTcpPorts
$script:n0dingPort = $ports[0]
$script:fixturePort = $ports[1]
$script:n0dingURL = "http://127.0.0.1:$script:n0dingPort"
$script:fixtureURL = "http://127.0.0.1:$script:fixturePort"
$script:httpClient = [Net.Http.HttpClient]::new()

try {
    foreach ($entry in $canaryValues.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value)
    }
    $env:N0DING_SOAK_IMAGE = $n0dingImage
    $env:N0DING_SOAK_FIXTURE_IMAGE = $fixtureImage
    $env:N0DING_SOAK_CONFIG = $soakConfig
    $env:N0DING_SOAK_CACHE_VOLUME = $cacheVolume
    $env:N0DING_SOAK_PORT = [string]$script:n0dingPort
    $env:N0DING_SOAK_FIXTURE_PORT = [string]$script:fixturePort
    $env:N0DING_SOAK_MAX_AGE = Format-GoDuration -Value $RetentionAge
    $env:N0DING_SOAK_GC_INTERVAL = Format-GoDuration -Value $GCInterval
    $env:N0DING_SOAK_STALE_TEMP_AGE = Format-GoDuration -Value $staleTempAge

    Invoke-Docker -Arguments @("version") -OutputPath (
        Join-Path $EvidenceRoot "docker-version.txt"
    )
    Invoke-Docker -Arguments @("compose", "version") -OutputPath (
        Join-Path $EvidenceRoot "compose-version.txt"
    )
    & docker volume inspect $cacheVolume *> $null
    if ($LASTEXITCODE -eq 0) {
        throw "Generated soak volume unexpectedly exists: $cacheVolume"
    }
    Invoke-Docker -Arguments @("volume", "create", $cacheVolume) -OutputPath (
        Join-Path $EvidenceRoot "volume-create.txt"
    )
    $volumeCreated = $true

    Invoke-Compose -Arguments @("config", "--quiet")
    Invoke-Compose -Arguments @(
        "up", "--build", "--detach", "--wait", "--wait-timeout", "240"
    )
    $stackStarted = $true
    Wait-Health

    $startedAt = [DateTimeOffset]::UtcNow
    $deadline = $startedAt + $Duration
    $null = Save-Snapshot -Label "start"
    $null = Invoke-Workload -Cycle "seed" -Expectation "cold"
    $warm = Save-Snapshot -Label "warm"
    $warmObjects = (
        (Get-Repository -Status $warm.status -Name "npm").cache_objects +
        (Get-Repository -Status $warm.status -Name "oci").cache_objects
    )
    if ($warmObjects -ne 4) {
        throw "Initial workload created $warmObjects complete objects, want 4."
    }

    Invoke-ClientAbort -Label "client-abort"
    Invoke-ControlledRestart -Label "forced-restart"
    $retentionPeak = Save-Snapshot -Label "retention-peak"
    $peakDiskKiB = [int64]$retentionPeak.volume.disk_kib

    Wait-ForcedExpiry -Deadline $deadline
    $expired = Save-Snapshot -Label "forced-expired"
    foreach ($name in @("npm", "oci")) {
        $repository = Get-Repository -Status $expired.status -Name $name
        if ($repository.cache_objects -ne 0 -or $repository.storage_bytes -ne 0) {
            throw "Forced expiry left complete $name objects or bytes."
        }
    }
    if ([int64]$expired.volume.disk_kib -ge $peakDiskKiB) {
        throw "Filesystem usage did not fall after forced expiry."
    }
    $null = Invoke-Workload -Cycle "seed" -Expectation "cold"
    $null = Save-Snapshot -Label "forced-refetch"

    $nextRestart = [DateTimeOffset]::MaxValue
    if ($RestartInterval -gt [TimeSpan]::Zero) {
        $nextRestart = [DateTimeOffset]::UtcNow + $RestartInterval
    }
    $cycle = 1
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        $cycleLabel = "loop-{0:D6}" -f $cycle
        $null = Invoke-Workload -Cycle $cycleLabel -Expectation "cold"
        $null = Save-Snapshot -Label $cycleLabel
        Write-Progress -StartedAt $startedAt -Deadline $deadline

        if ([DateTimeOffset]::UtcNow -ge $nextRestart) {
            Invoke-ControlledRestart -Label "scheduled-restart-$cycleLabel"
            $nextRestart = [DateTimeOffset]::UtcNow + $RestartInterval
        }
        $cycle++
        $remaining = $deadline - [DateTimeOffset]::UtcNow
        if ($remaining -le [TimeSpan]::Zero) {
            break
        }
        $sleep = [Math]::Min($SampleInterval.TotalSeconds, $remaining.TotalSeconds)
        Start-Sleep -Milliseconds ([int]($sleep * 1000))
    }

    $final = Save-Snapshot -Label "final"
    $serverCounters = Get-AggregateServerCounters -FinalStatus $final.status
    Save-CacheSnapshot
    $cacheCheck = Test-ExportedCache

    $logText = Get-Content -LiteralPath (Join-Path $EvidenceRoot "compose.log") -Raw
    if ($logText -notmatch "stale cache temporary files removed") {
        throw "Restart did not record stale temporary-file cleanup."
    }
    if ($logText -notmatch "cache GC completed") {
        throw "Soak did not record a cache GC deletion."
    }
    $warningCount = ([regex]::Matches($logText, '"level":"WARN"')).Count
    $errorCount = ([regex]::Matches($logText, '"level":"ERROR"')).Count

    $finishedAt = [DateTimeOffset]::UtcNow
    $elapsed = $finishedAt - $startedAt
    $sevenDayCompleted = (
        $Mode -eq "SevenDay" -and
        $Duration -ge [TimeSpan]::FromDays(7) -and
        $elapsed -ge [TimeSpan]::FromDays(7)
    )
    $result = [ordered]@{
        status                       = "pending_canary_scan"
        mode                         = $Mode
        source_commit                = (& git -C $repositoryRoot rev-parse HEAD).Trim()
        started_at                   = $startedAt.ToString("o")
        finished_at                  = $finishedAt.ToString("o")
        requested_duration_seconds   = [int64]$Duration.TotalSeconds
        elapsed_seconds              = [Math]::Round($elapsed.TotalSeconds, 3)
        seven_day_completed          = $sevenDayCompleted
        workers_per_cache_key        = $Workers
        workload_cycles              = $script:cycles
        controlled_restarts          = $script:restartCount
        client_cache_hits            = $script:clientHits
        client_cache_misses          = $script:clientMisses
        denied_identity_checks       = $script:deniedChecks
        server_counters              = $serverCounters
        retention_age_seconds        = [int64]$RetentionAge.TotalSeconds
        gc_interval_seconds          = [int64]$GCInterval.TotalSeconds
        disk_kib_at_retention_peak   = $peakDiskKiB
        disk_kib_after_forced_expiry = [int64]$expired.volume.disk_kib
        max_disk_kib                 = $script:maxDiskKiB
        final_complete_objects       = $cacheCheck.objects
        final_temp_files             = $cacheCheck.temps
        log_warnings                 = $warningCount
        log_errors                   = $errorCount
        status_metrics_consistency   = "pass"
        identity_safety              = "pass"
        client_abort_cleanup         = "pass"
        forced_restart_cleanup       = "pass"
        forced_expiry_refetch        = "pass"
        cache_pair_integrity         = "pass"
        credential_canary_scan       = "pending"
        artifact_path                = $EvidenceRoot
    }
    $resultPath = Join-Path $EvidenceRoot "result.json"
    $result | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $resultPath

    & $canaryScanner -LiteralPath $EvidenceRoot -CanaryEnv @($canaryValues.Keys)
    if (-not $?) {
        throw "Credential canary scan failed."
    }
    $result["status"] = "pass"
    $result["credential_canary_scan"] = "pass"
    $result | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $resultPath

    Write-Output "Retention/concurrency soak passed for the requested duration."
    Write-Output "Evidence: $EvidenceRoot"
    $result | Format-List
} catch {
    $failure = [ordered]@{
        status        = "fail"
        mode          = $Mode
        source_commit = (& git -C $repositoryRoot rev-parse HEAD).Trim()
        failed_at     = [DateTimeOffset]::UtcNow.ToString("o")
        error         = $_.Exception.Message
        artifact_path = $EvidenceRoot
    }
    $failure | ConvertTo-Json |
        Set-Content -LiteralPath (Join-Path $EvidenceRoot "failure.json")
    if ($stackStarted) {
        try {
            Invoke-Compose -Arguments @("logs", "--no-color") -OutputPath (
                Join-Path $EvidenceRoot "compose-failure.log"
            )
        } catch {
        }
    }
    throw
} finally {
    $script:httpClient.Dispose()
    & docker compose --file $composeFile --project-name $project `
        down --remove-orphans *> $null
    if ($volumeCreated) {
        & docker volume rm $cacheVolume *> $null
    }
    & docker image rm $n0dingImage $fixtureImage *> $null

    foreach ($name in $managedEnvironment) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name])
    }
}

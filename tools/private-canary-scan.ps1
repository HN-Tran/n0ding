[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string[]]$LiteralPath,

    [Parameter(Mandatory = $true)]
    [string[]]$CanaryEnv
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Find-CanariesInFile {
    param(
        [IO.FileInfo]$File,
        [object[]]$Canaries
    )

    $matches = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::Ordinal
    )
    $maximumLength = 0
    foreach ($canary in $Canaries) {
        $maximumLength = [Math]::Max($maximumLength, $canary.Value.Length)
    }
    $encoding = [Text.UTF8Encoding]::new($false, $false)
    $reader = [IO.StreamReader]::new($File.FullName, $encoding, $true, 65536)
    try {
        $buffer = [char[]]::new(65536)
        $carry = ""
        while (($read = $reader.ReadBlock($buffer, 0, $buffer.Length)) -gt 0) {
            $chunk = $carry + [string]::new($buffer, 0, $read)
            foreach ($canary in $Canaries) {
                if ($chunk.IndexOf($canary.Value, [StringComparison]::Ordinal) -ge 0) {
                    [void]$matches.Add($canary.Name)
                }
            }
            $carryLength = [Math]::Min($maximumLength - 1, $chunk.Length)
            if ($carryLength -gt 0) {
                $carry = $chunk.Substring($chunk.Length - $carryLength)
            } else {
                $carry = ""
            }
        }
    } finally {
        $reader.Dispose()
    }
    return $matches
}

$canaries = @(foreach ($name in $CanaryEnv) {
    if ($name -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') {
        throw "Invalid environment variable name: $name"
    }
    $value = [Environment]::GetEnvironmentVariable($name)
    if ([string]::IsNullOrWhiteSpace($value)) {
        throw "Canary environment variable is unset or empty: $name"
    }
    if ($value.Length -lt 8) {
        throw "Canary environment variable must contain at least 8 characters: $name"
    }
    [pscustomobject]@{
        Name  = $name
        Value = $value
    }
})

$files = [Collections.Generic.List[IO.FileInfo]]::new()
foreach ($requestedPath in $LiteralPath) {
    $resolved = Resolve-Path -LiteralPath $requestedPath
    $item = Get-Item -LiteralPath $resolved.Path
    if ($item -is [IO.DirectoryInfo]) {
        foreach ($file in Get-ChildItem -LiteralPath $item.FullName -File -Recurse) {
            $files.Add($file)
        }
    } else {
        $files.Add($item)
    }
}
if ($files.Count -eq 0) {
    throw "No files found under the requested paths."
}

$findings = 0
foreach ($file in $files) {
    $matches = Find-CanariesInFile -File $file -Canaries $canaries
    foreach ($name in $matches) {
        Write-Error "Credential canary $name found in $($file.FullName)" -ErrorAction Continue
        $findings++
    }
}

if ($findings -ne 0) {
    throw "Credential canary scan failed with $findings finding(s)."
}

Write-Output "Credential canary scan passed: $($files.Count) file(s), $($canaries.Count) canary value(s)."

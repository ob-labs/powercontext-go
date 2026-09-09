[CmdletBinding(DefaultParameterSetName = 'Build')]
param(
  [Parameter(Mandatory, ParameterSetName = 'Build')]
  [string]$Syft,
  [Parameter(Mandatory, ParameterSetName = 'Published')]
  [string]$Archive,
  [Parameter(Mandatory, ParameterSetName = 'Published')]
  [string]$SBOM,
  [Parameter(Mandatory, ParameterSetName = 'Published')]
  [string]$ExpectedVersion,
  [Parameter(Mandatory, ParameterSetName = 'Published')]
  [string]$ExpectedCommit,
  [Parameter(Mandatory, ParameterSetName = 'Published')]
  [Parameter(Mandatory, ParameterSetName = 'Metadata')]
  [string]$ExpectedBuildDate,
  [Parameter(Mandatory, ParameterSetName = 'Metadata')]
  [string]$BuildInfoJSON
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-Native {
  param(
    [Parameter(Mandatory)]
    [string]$Name,
    [Parameter(Mandatory)]
    [scriptblock]$Command
  )

  $output = & $Command 2>&1
  if ($LASTEXITCODE -ne 0) {
    throw "$Name failed with exit code ${LASTEXITCODE}:`n$($output | Out-String)"
  }
  return ($output | Out-String)
}

function Invoke-Server {
  param(
    [Parameter(Mandatory)]
    [string]$Binary,
    [Parameter(Mandatory)]
    [string[]]$Arguments
  )

  $output = Invoke-Native -Name "powercontext $($Arguments -join ' ')" -Command {
    & $Binary --json @Arguments
  }
  return $output | ConvertFrom-Json
}

function Assert-Equal {
  param(
    [Parameter(Mandatory)]
    [string]$Name,
    [Parameter(Mandatory)]
    [string]$Actual,
    [Parameter(Mandatory)]
    [string]$Expected
  )

  if ($Actual -cne $Expected) {
    throw "$Name = $Actual, want $Expected"
  }
}

function Assert-BuildDate {
  param(
    [Parameter(Mandatory)]
    [object]$Actual,
    [Parameter(Mandatory)]
    [string]$Expected
  )

  try {
    $actualInstant = ([DateTimeOffset]$Actual).ToUniversalTime()
    $dateStyles = [Globalization.DateTimeStyles]::AssumeUniversal -bor [Globalization.DateTimeStyles]::AdjustToUniversal
    $expectedInstant = [DateTimeOffset]::ParseExact(
      $Expected,
      "yyyy-MM-dd'T'HH:mm:ss'Z'",
      [Globalization.CultureInfo]::InvariantCulture,
      $dateStyles
    )
  } catch {
    throw 'build manifest date is not a valid UTC instant'
  }
  if ($actualInstant -ne $expectedInstant) {
    throw 'build manifest date does not match the expected UTC instant'
  }
}

if ($PSCmdlet.ParameterSetName -eq 'Metadata') {
  $metadata = $BuildInfoJSON | ConvertFrom-Json
  Assert-BuildDate -Actual $metadata.build_date -Expected $ExpectedBuildDate
  return
}

function Wait-Live {
  param([Parameter(Mandatory)][string]$Endpoint)

  $deadline = [DateTime]::UtcNow.AddSeconds(45)
  do {
    try {
      $response = Invoke-WebRequest -NoProxy -UseBasicParsing -TimeoutSec 2 -Uri "$Endpoint/health/live"
      if ($response.StatusCode -eq 200 -and $response.Content.Trim() -eq '{"status":"ok"}') {
        return
      }
    } catch {
    }
    Start-Sleep -Milliseconds 250
  } while ([DateTime]::UtcNow -lt $deadline)
  throw "PowerContext did not become live at $Endpoint"
}

function Wait-Unreachable {
  param([Parameter(Mandatory)][string]$Endpoint)

  $deadline = [DateTime]::UtcNow.AddSeconds(30)
  do {
    try {
      $response = Invoke-WebRequest -NoProxy -UseBasicParsing -TimeoutSec 2 -Uri "$Endpoint/health/live"
      if ($response.StatusCode -ne 200) {
        return
      }
    } catch {
      return
    }
    Start-Sleep -Milliseconds 250
  } while ([DateTime]::UtcNow -lt $deadline)
  throw "PowerContext remained live at $Endpoint after uninstall"
}

function Wait-ArchiveProcessExit {
  param([Parameter(Mandatory)][string]$Binary)

  $expected = [IO.Path]::GetFullPath($Binary)
  $deadline = [DateTime]::UtcNow.AddSeconds(30)
  do {
    $running = @(
      Get-Process -Name powercontext -ErrorAction SilentlyContinue | Where-Object {
        try {
          [string]::Equals([IO.Path]::GetFullPath($_.Path), $expected, [StringComparison]::OrdinalIgnoreCase)
        } catch {
          $false
        }
      }
    )
    if ($running.Count -eq 0) {
      return
    }
    Start-Sleep -Milliseconds 250
  } while ([DateTime]::UtcNow -lt $deadline)
  throw 'archived PowerContext process remained after uninstall'
}

function Get-FreeLoopbackPort {
  $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
  try {
    $listener.Start()
    return $listener.LocalEndpoint.Port
  } finally {
    $listener.Stop()
  }
}

$repository = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$published = $PSCmdlet.ParameterSetName -eq 'Published'
if ($published) {
  $archivePath = [IO.Path]::GetFullPath($Archive)
  $sbomPath = [IO.Path]::GetFullPath($SBOM)
  foreach ($inputPath in @($archivePath, $sbomPath)) {
    if (!(Test-Path -LiteralPath $inputPath -PathType Leaf)) {
      throw 'published Windows release input is unavailable'
    }
    $relative = [IO.Path]::GetRelativePath($repository, $inputPath)
    if (![IO.Path]::IsPathRooted($relative) -and $relative -ne '..' -and !$relative.StartsWith("..$([IO.Path]::DirectorySeparatorChar)", [StringComparison]::Ordinal)) {
      throw 'published Windows release input must not come from the source checkout'
    }
  }
  if ($ExpectedVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$' -or $ExpectedVersion.Length -gt 80) {
    throw 'expected release version is invalid'
  }
  if ($ExpectedCommit -notmatch '^[0-9a-f]{40}$') {
    throw 'expected release commit is invalid'
  }
  if ($ExpectedBuildDate -notmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$') {
    throw 'expected release build date is invalid'
  }
  $version = $ExpectedVersion
  $commit = $ExpectedCommit
  $buildDate = $ExpectedBuildDate
} else {
  $syftPath = [IO.Path]::GetFullPath($Syft)
  if (!(Test-Path -LiteralPath $syftPath -PathType Leaf)) {
    throw 'pinned Syft executable is unavailable'
  }
  $commit = (Invoke-Native -Name 'read source commit' -Command { git -C $repository rev-parse HEAD }).Trim()
  if ($commit -notmatch '^[0-9a-f]{40}$') {
    throw 'source commit is invalid'
  }
  $epoch = [Int64]((Invoke-Native -Name 'read source timestamp' -Command { git -C $repository show -s --format=%ct $commit }).Trim())
  $buildDate = [DateTimeOffset]::FromUnixTimeSeconds($epoch).UtcDateTime.ToString('yyyy-MM-ddTHH:mm:ssZ')
  $runIdentity = if ($env:GITHUB_RUN_ID) { $env:GITHUB_RUN_ID } else { [DateTime]::UtcNow.Ticks }
  $version = "0.0.0-windows.consumer.$runIdentity"
}
$workRoot = Join-Path ([IO.Path]::GetTempPath()) ("powercontext-windows-release-consumer-" + [Guid]::NewGuid().ToString('N'))
$taskName = '\PowerContext Personal Server'
$archiveBinary = ''
$dataDirectory = ''

try {
  New-Item -ItemType Directory -Path $workRoot | Out-Null
  $buildDirectory = Join-Path $workRoot 'build'
  $distribution = Join-Path $workRoot 'dist'
  $extractDirectory = Join-Path $workRoot 'extract'
  $builtBinary = Join-Path $buildDirectory 'powercontext.exe'
  $dataDirectory = Join-Path $workRoot 'data'
  $environmentFile = Join-Path $workRoot 'server.env'
  $port = Get-FreeLoopbackPort
  $endpoint = "http://127.0.0.1:$port"
  New-Item -ItemType Directory -Path $buildDirectory, $distribution, $extractDirectory | Out-Null
  @(
    'POWERCONTEXT_SERVER_HTTP_HOST=127.0.0.1',
    "POWERCONTEXT_SERVER_HTTP_PORT=$port",
    'POWERCONTEXT_SERVER_AUTH_ENABLED=false',
    'POWERCONTEXT_SERVER_MCP_ENABLED=false',
    'POWERCONTEXT_SERVER_RUNTIME_SOURCE_WINDOW_LIMIT=1'
  ) | Set-Content -LiteralPath $environmentFile -Encoding ascii

  if (!$published) {
    Push-Location $repository
    try {
      Invoke-Native -Name 'build Windows standard release binary' -Command {
        $env:CGO_ENABLED = '1'
        & go build -tags sqlite_fts5 -trimpath `
          -ldflags "-s -w -X main.version=$version -X main.commit=$commit -X main.date=$buildDate" `
          -o $builtBinary ./cmd/powercontext
      } | Out-Null
      Invoke-Native -Name 'package Windows standard release archive' -Command {
        & go run ./tools/release package `
          -binary $builtBinary -edition standard -version $version -commit $commit -build-date $buildDate `
          -output $distribution -syft $syftPath
      } | Out-Null
    } finally {
      Pop-Location
    }

    $archives = @(Get-ChildItem -LiteralPath $distribution -Filter '*.tar.gz' -File)
    if ($archives.Count -ne 1) {
      throw "standard release archive count = $($archives.Count), want 1"
    }
    $archivePath = $archives[0].FullName
  }

  Invoke-Native -Name 'extract Windows standard release archive' -Command {
    & tar.exe -xzf $archivePath -C $extractDirectory
  } | Out-Null
  $releaseRoots = @(Get-ChildItem -LiteralPath $extractDirectory -Directory)
  if ($releaseRoots.Count -ne 1) {
    throw "release archive root count = $($releaseRoots.Count), want 1"
  }
  $releaseRoot = $releaseRoots[0].FullName
  $archiveBinary = Join-Path $releaseRoot 'bin\powercontext.exe'
  if (!(Test-Path -LiteralPath $archiveBinary -PathType Leaf)) {
    throw 'release archive does not contain bin/powercontext.exe'
  }
  if (Test-Path -LiteralPath (Join-Path $releaseRoot 'bin\powercontext')) {
    throw 'Windows release archive retained an extensionless binary'
  }
  $buildInfo = Get-Content -Raw -LiteralPath (Join-Path $releaseRoot 'BUILD-INFO.json') | ConvertFrom-Json
  foreach ($assertion in @(
    @{ Name = 'build manifest product'; Actual = $buildInfo.product; Expected = 'PowerContext' },
    @{ Name = 'build manifest edition'; Actual = $buildInfo.edition; Expected = 'standard' },
    @{ Name = 'build manifest version'; Actual = $buildInfo.version; Expected = $version },
    @{ Name = 'build manifest commit'; Actual = $buildInfo.commit; Expected = $commit },
    @{ Name = 'build manifest target'; Actual = $buildInfo.target; Expected = 'windows-amd64' },
    @{ Name = 'build manifest binary path'; Actual = $buildInfo.binary.path; Expected = 'bin/powercontext.exe' }
  )) {
    Assert-Equal -Name $assertion.Name -Actual $assertion.Actual -Expected $assertion.Expected
  }
  Assert-BuildDate -Actual $buildInfo.build_date -Expected $buildDate
  if ($buildInfo.cgo_enabled -ne $true) {
    throw 'build manifest does not record CGO-enabled Windows release bytes'
  }
  Assert-Equal -Name 'archive binary hash' -Actual (Get-FileHash -Algorithm SHA256 -LiteralPath $archiveBinary).Hash.ToLowerInvariant() -Expected $buildInfo.binary.sha256

  if ($published) {
    Push-Location $repository
    try {
      Invoke-Native -Name 'verify published Windows release evidence' -Command {
        & go run ./tools/release verify-evidence -root $releaseRoot -repository $repository -sbom $sbomPath
      } | Out-Null
    } finally {
      Pop-Location
    }
  }

  $install = Invoke-Server -Binary $archiveBinary -Arguments @('server', 'install', '--env-file', $environmentFile, '--data-dir', $dataDirectory)
  Assert-Equal -Name 'install support' -Actual $install.support -Expected 'supported'
  Wait-Live -Endpoint $endpoint

  $status = Invoke-Server -Binary $archiveBinary -Arguments @('server', 'status', '--data-dir', $dataDirectory)
  foreach ($assertion in @(
    @{ Name = 'status support'; Actual = $status.support; Expected = 'supported' },
    @{ Name = 'status registration'; Actual = $status.registration; Expected = 'installed' },
    @{ Name = 'status definition'; Actual = $status.definition; Expected = 'current' },
    @{ Name = 'status manager ownership'; Actual = $status.manager_ownership; Expected = 'owned' },
    @{ Name = 'status manager'; Actual = $status.manager; Expected = 'active' },
    @{ Name = 'status liveness'; Actual = $status.liveness; Expected = 'live' }
  )) {
    Assert-Equal -Name $assertion.Name -Actual $assertion.Actual -Expected $assertion.Expected
  }

  [xml]$taskDocument = Invoke-Native -Name 'inspect fixed PowerContext task' -Command {
    & schtasks.exe /Query /TN $taskName /XML
  }
  $namespace = [Xml.XmlNamespaceManager]::new($taskDocument.NameTable)
  $namespace.AddNamespace('task', 'http://schemas.microsoft.com/windows/2004/02/mit/task')
  $command = $taskDocument.SelectSingleNode('/task:Task/task:Actions/task:Exec/task:Command', $namespace)
  if ($null -eq $command) {
    throw 'fixed PowerContext task has no executable command'
  }
  $actualBinary = [IO.Path]::GetFullPath($command.InnerText)
  $expectedBinary = [IO.Path]::GetFullPath($archiveBinary)
  if (![string]::Equals($actualBinary, $expectedBinary, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'fixed PowerContext task does not execute the archived binary'
  }
  $taskXML = $taskDocument.OuterXml
  if ($taskXML.Contains($repository, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'fixed PowerContext task retained a source checkout path'
  }

  $database = Join-Path $dataDirectory 'powercontext.db'
  if (!(Test-Path -LiteralPath $database -PathType Leaf)) {
    throw 'personal service did not create its SQLite database'
  }

  $uninstall = Invoke-Server -Binary $archiveBinary -Arguments @('server', 'uninstall', '--data-dir', $dataDirectory)
  Assert-Equal -Name 'uninstall registration' -Actual $uninstall.registration -Expected 'not_installed'
  Wait-ArchiveProcessExit -Binary $archiveBinary
  $header = [Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes($database), 0, 16)
  Assert-Equal -Name 'personal service SQLite header' -Actual $header -Expected "SQLite format 3$([char]0)"
  Wait-Unreachable -Endpoint $endpoint
  $postUninstall = Invoke-Server -Binary $archiveBinary -Arguments @('server', 'status', '--data-dir', $dataDirectory)
  Assert-Equal -Name 'post-uninstall registration' -Actual $postUninstall.registration -Expected 'not_installed'

  & schtasks.exe /Query /TN $taskName /XML 2>&1 | Out-Null
  if ($LASTEXITCODE -eq 0) {
    throw 'fixed PowerContext task remains after uninstall'
  }
} finally {
  if ($archiveBinary -and (Test-Path -LiteralPath $archiveBinary -PathType Leaf) -and $dataDirectory) {
    & $archiveBinary server uninstall --data-dir $dataDirectory 2>&1 | Out-Null
    Wait-ArchiveProcessExit -Binary $archiveBinary
  }
  if (Test-Path -LiteralPath $workRoot) {
    Remove-Item -LiteralPath $workRoot -Recurse -Force
  }
}

# Install the wfx client on Windows. The same behaviour as install.sh.
#
#   irm https://raw.githubusercontent.com/muthuishere/wfnexus/main/install.ps1 | iex
#   $env:WFX_VERSION='v0.1.0'; ./install.ps1        # pin a version
#
# Per-user only: it installs to %LOCALAPPDATA%\wfx\bin and never asks for
# administrator rights. It verifies the download against the release's
# checksums.txt and REFUSES to install on a mismatch or a missing checksum.
#
# There is deliberately no winget manifest and no Chocolatey package yet — see
# the install documentation.

$ErrorActionPreference = 'Stop'

$repo        = if ($env:WFX_REPO) { $env:WFX_REPO } else { 'muthuishere/wfnexus' }
$base        = if ($env:WFX_BASE_URL) { $env:WFX_BASE_URL } else { "https://github.com/$repo/releases" }
$installDir  = if ($env:WFX_INSTALL_DIR) { $env:WFX_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'wfx\bin' }

switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { $goarch = 'amd64' }
  'ARM64' { $goarch = 'arm64' }
  default {
    throw "wfx install: unsupported architecture '$env:PROCESSOR_ARCHITECTURE' (detected windows/$env:PROCESSOR_ARCHITECTURE).`n  Published platforms: windows/amd64, windows/arm64."
  }
}

if ($env:WFX_VERSION) { $version = $env:WFX_VERSION; $url = "$base/download/$version" }
else                  { $version = 'latest';        $url = "$base/latest/download" }

$asset = "wfx-windows-$goarch.exe"
$tmp   = Join-Path ([System.IO.Path]::GetTempPath()) ("wfx-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
  Write-Host "wfx: windows/$goarch, $version"

  # The checksum file first: no checksums, no install.
  $sumFile = Join-Path $tmp 'checksums.txt'
  try { Invoke-WebRequest -Uri "$url/checksums.txt" -OutFile $sumFile -UseBasicParsing }
  catch { throw "wfx install: could not download $url/checksums.txt - refusing to install unverified" }

  $line = Select-String -Path $sumFile -Pattern ("[ \*]" + [regex]::Escape($asset) + "$") | Select-Object -First 1
  if (-not $line) { throw "wfx install: no checksum published for '$asset' - refusing to install unverified" }
  $want = ($line.Line -split '\s+')[0].ToLower()

  $dl = Join-Path $tmp $asset
  try { Invoke-WebRequest -Uri "$url/$asset" -OutFile $dl -UseBasicParsing }
  catch { throw "wfx install: download failed: $url/$asset" }
  if ((Get-Item $dl).Length -eq 0) { throw "wfx install: downloaded file is empty - a partial download, nothing installed" }

  $got = (Get-FileHash -Path $dl -Algorithm SHA256).Hash.ToLower()
  if ($got -ne $want) {
    Remove-Item $dl -Force
    throw "wfx install: CHECKSUM MISMATCH for $asset`n  published: $want`n  download:  $got`n  Nothing was installed and the download has been deleted."
  }
  Write-Host "verified sha256 $got"

  New-Item -ItemType Directory -Force -Path $installDir | Out-Null
  # Move into place in one step so an interrupted install cannot leave a
  # half-written wfx.exe on the path.
  Move-Item -Path $dl -Destination (Join-Path $installDir 'wfx.exe') -Force
  Write-Host "installed $installDir\wfx.exe"

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($userPath -split ';') -notcontains $installDir) {
    Write-Host ""
    Write-Host "$installDir is not on your PATH. Add it:"
    Write-Host "  [Environment]::SetEnvironmentVariable('Path', `"`$env:Path;$installDir`", 'User')"
  }

  Write-Host ""
  Write-Host "try: wfx version"
}
finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

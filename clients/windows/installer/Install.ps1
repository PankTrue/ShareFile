param([switch]$StartWithWindows)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$source = $PSScriptRoot
$target = Join-Path $env:LOCALAPPDATA 'Programs/ShareFile'
$exe = Join-Path $target 'ShareFile.exe'
$marker = Join-Path $target '.sharefile-install'
$identity = 'ShareFile.Windows.v1'
if (-not (Test-Path (Join-Path $source 'ShareFile.exe'))) { throw 'Extract the complete ShareFile Windows archive first.' }
if ([IO.Path]::GetFullPath($source).TrimEnd('\') -eq [IO.Path]::GetFullPath($target).TrimEnd('\')) {
    throw 'Run the installer from a freshly extracted release, outside the installation directory.'
}
if (Test-Path $target) {
    if (-not (Test-Path $marker) -or [IO.File]::ReadAllText($marker) -ne $identity) {
        throw 'The target directory is not a managed ShareFile installation. Move it before installing.'
    }
    $running = @(Get-Process -Name ShareFile -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe })
    if ($running.Count -gt 0) { throw 'Exit ShareFile using its tray menu before updating.' }
}
New-Item -ItemType Directory -Force -Path (Split-Path $target) | Out-Null
$stage = "$target.stage-$([Guid]::NewGuid().ToString('N'))"
$backup = $null
try {
    New-Item -ItemType Directory -Path $stage | Out-Null
    Copy-Item -Path "$source/*" -Destination $stage -Recurse
    [IO.File]::WriteAllText((Join-Path $stage '.sharefile-install'), $identity)
    if (Test-Path $target) {
        $backup = "$target.previous-$([DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss'))-$([Guid]::NewGuid().ToString('N').Substring(0,8))"
        Move-Item -LiteralPath $target -Destination $backup
        Write-Output "Previous version saved: $backup"
    }
    try { Move-Item -LiteralPath $stage -Destination $target }
    catch { if ($backup -and -not (Test-Path $target)) { Move-Item -LiteralPath $backup -Destination $target }; throw }
} finally { if (Test-Path $stage) { Remove-Item -LiteralPath $stage -Recurse -Force } }
$shell = New-Object -ComObject WScript.Shell
try {
    $menu = Join-Path ([Environment]::GetFolderPath('Programs')) 'ShareFile.lnk'
    $shortcut = $shell.CreateShortcut($menu)
    $shortcut.TargetPath = $exe; $shortcut.WorkingDirectory = $target; $shortcut.IconLocation = "$exe,0"; $shortcut.Save()
    $sendTo = Join-Path ([Environment]::GetFolderPath('SendTo')) 'ShareFile - Upload and share.lnk'
    $shortcut = $shell.CreateShortcut($sendTo)
    $shortcut.TargetPath = $exe; $shortcut.Arguments = '--upload'; $shortcut.WorkingDirectory = $target
    $shortcut.IconLocation = "$exe,0"; $shortcut.Save()
} finally { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) | Out-Null }
if ($StartWithWindows) {
    New-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name ShareFile -Value "`"$exe`" --background" -PropertyType String -Force | Out-Null
}
Write-Output "Installed: $target"
Write-Output 'Explorer: right click selected files > Show more options > Send to > ShareFile - Upload and share'
Start-Process -FilePath $exe

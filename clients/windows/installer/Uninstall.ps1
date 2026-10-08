$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$target = Join-Path $env:LOCALAPPDATA 'Programs/ShareFile'
$exe = Join-Path $target 'ShareFile.exe'
$marker = Join-Path $target '.sharefile-install'
if (-not (Test-Path $marker) -or [IO.File]::ReadAllText($marker) -ne 'ShareFile.Windows.v1') {
    throw 'No managed ShareFile installation found.'
}
$running = @(Get-Process -Name ShareFile -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe })
if ($running.Count -gt 0) { throw 'Exit ShareFile using its tray menu before uninstalling.' }
$shell = New-Object -ComObject WScript.Shell
try {
    foreach ($link in @((Join-Path ([Environment]::GetFolderPath('Programs')) 'ShareFile.lnk'),
        (Join-Path ([Environment]::GetFolderPath('SendTo')) 'ShareFile - Upload and share.lnk'))) {
        if ((Test-Path $link) -and $shell.CreateShortcut($link).TargetPath -eq $exe) { Remove-Item -LiteralPath $link }
    }
} finally { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) | Out-Null }
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$startup = Get-ItemProperty -Path $runKey -Name ShareFile -ErrorAction SilentlyContinue
if ($startup -and $startup.ShareFile -eq "`"$exe`" --background") { Remove-ItemProperty -Path $runKey -Name ShareFile }
Remove-Item -LiteralPath $target -Recurse -Force
Write-Output 'ShareFile removed. Queue, credentials and server files were preserved.'

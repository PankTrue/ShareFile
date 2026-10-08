param([ValidateSet('win-x64', 'win-arm64')][string[]]$Runtime = @('win-x64', 'win-arm64'))
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
Push-Location $PSScriptRoot
try {
    foreach ($rid in $Runtime) {
        $output = Join-Path $PSScriptRoot "build/$rid"
        if (Test-Path $output) { Remove-Item -LiteralPath $output -Recurse -Force }
        & dotnet publish src/ShareFile.Windows/ShareFile.Windows.csproj -c Release -r $rid --self-contained true -p:RestoreLockedMode=true -o $output
        if ($LASTEXITCODE -ne 0) { throw "Publishing $rid failed" }
        Copy-Item installer/* $output
        Copy-Item ../../docs/windows.md "$output/README.md"
        Copy-Item ../../LICENSE "$output/LICENSE.txt"
        $packagesLine = & dotnet nuget locals global-packages --list --force-english-output
        if ($LASTEXITCODE -ne 0) { throw 'Cannot locate dependency licenses' }
        $packagesRoot = ($packagesLine -replace '^global-packages:\s*', '').Trim()
        $frameworks = (Get-Content "$output/ShareFile.runtimeconfig.json" -Raw | ConvertFrom-Json).runtimeOptions.includedFrameworks
        [xml]$project = Get-Content src/ShareFile.Windows/ShareFile.Windows.csproj -Raw
        $mvvmVersion = ($project.Project.ItemGroup.PackageReference | Where-Object { $_.Include -eq 'CommunityToolkit.Mvvm' }).Version
        $licenses = Join-Path $output 'licenses'
        New-Item -ItemType Directory -Path $licenses | Out-Null
        $dependencies = @(
            @{ Package = "microsoft.netcore.app.runtime.$rid"; Version = ($frameworks | Where-Object name -eq 'Microsoft.NETCore.App').version },
            @{ Package = "microsoft.windowsdesktop.app.runtime.$rid"; Version = ($frameworks | Where-Object name -eq 'Microsoft.WindowsDesktop.App').version },
            @{ Package = 'communitytoolkit.mvvm'; Version = $mvvmVersion }
        )
        foreach ($dependency in $dependencies) {
            $packageDirectory = Join-Path $packagesRoot "$($dependency.Package)/$($dependency.Version)"
            foreach ($license in Get-ChildItem -LiteralPath $packageDirectory -File | Where-Object Name -Match 'LICENSE|Third.?Party.?Notices') {
                Copy-Item -LiteralPath $license.FullName -Destination (Join-Path $licenses "$($dependency.Package)-$($license.Name)")
            }
        }
        $archive = Join-Path $PSScriptRoot "build/ShareFile-Windows-$rid.zip"
        Compress-Archive -Path "$output/*" -DestinationPath $archive -Force
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant()
        [IO.File]::WriteAllText("$archive.sha256", "$hash  $([IO.Path]::GetFileName($archive))`n", [Text.Encoding]::ASCII)
        Write-Output $archive
    }
} finally { Pop-Location }

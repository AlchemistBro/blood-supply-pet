# Проверки отдельного PostgreSQL без Docker и развёртывания стенда. Секреты не выводятся.
param(
    [string]$EnvFile = '.env',
    [string[]]$Packages = @('./libs/...', './services/...'),
    [string]$Run = '.'
)

$ErrorActionPreference = 'Stop'
$values = @{}
foreach ($line in [System.IO.File]::ReadAllLines((Resolve-Path -LiteralPath $EnvFile).ProviderPath)) {
    if ($line -match '^\s*(?:export\s+)?(DATABASE_PUBLIC_URL|BS_TEST_DB_URL|DATABASE_URL)\s*=\s*(.*)\s*$') {
        $key, $value = $Matches[1], $Matches[2].Trim()
        if (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'"))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        $values[$key] = $value
    }
}
$connection = $null
foreach ($key in @('DATABASE_PUBLIC_URL', 'BS_TEST_DB_URL', 'DATABASE_URL')) {
    if ($values[$key]) {
        $connection = $values[$key]
        break
    }
}
if (-not $connection) {
    throw 'В файле окружения нет URL PostgreSQL'
}
$previousURL, $previousGuard = $env:BS_TEST_DB_URL, $env:BS_TEST_NO_DOCKER
$previousEncoding = [Console]::OutputEncoding
try {
    [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
    $env:BS_TEST_DB_URL = $connection
    $env:BS_TEST_NO_DOCKER = 'true'
    & go test -tags integration -count=1 -p 2 -parallel 2 -timeout 25m -run $Run @Packages
    $testExit = $LASTEXITCODE
} finally {
    [Console]::OutputEncoding = $previousEncoding
    $env:BS_TEST_DB_URL = $previousURL
    $env:BS_TEST_NO_DOCKER = $previousGuard
}
exit $testExit

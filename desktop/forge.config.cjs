const path = require('node:path')

module.exports = {
  packagerConfig: {
    name: 'scp-client',
    executableName: 'scp-client',
    appBundleId: 'io.github.bustanil.scp-client',
    appCategoryType: 'public.app-category.utilities',
    asar: true,
    icon: path.join(__dirname, 'build', 'icon.icns'),
    extraResource: [path.join(__dirname, 'build', 'backend')],
    // Seal the final bundle after Packager replaces Electron's name and resources.
    // Ad-hoc signing verifies integrity; Developer ID and notarization establish trust.
    osxSign: {
      identity: '-',
      identityValidation: false,
      preAutoEntitlements: false,
      preEmbedProvisioningProfile: false,
      // Ad-hoc signatures have no Team ID for hardened runtime library validation.
      optionsForFile: () => ({ timestamp: 'none', hardenedRuntime: false }),
    },
    ignore: [/^\/build($|\/)/, /^\/out($|\/)/, /^\/scripts($|\/)/, /^\/tests($|\/)/, /^\/test-results($|\/)/, /^\/playwright/],
  },
  makers: [{ name: '@electron-forge/maker-dmg', config: { name: 'scp-client', format: 'ULFO' } }],
}

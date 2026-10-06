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
    ignore: [/^\/build($|\/)/, /^\/out($|\/)/, /^\/scripts($|\/)/, /^\/tests($|\/)/, /^\/test-results($|\/)/, /^\/playwright/],
  },
  makers: [{ name: '@electron-forge/maker-dmg', config: { name: 'scp-client', format: 'ULFO' } }],
}

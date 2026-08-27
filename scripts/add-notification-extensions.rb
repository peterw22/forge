#!/usr/bin/env ruby
require 'xcodeproj'

def add_extension(project_path:, platform:, deployment:, app_target_name: 'Runner')
  project = Xcodeproj::Project.open(project_path)
  return if project.targets.any? { |target| target.name == 'ForgeNotificationService' }
  app = project.targets.find { |target| target.name == app_target_name } or raise 'Runner target missing'
  source_root = File.dirname(project_path)
  group = project.main_group.find_subpath('ForgeNotificationService', true)
  group.set_source_tree('<group>')
  group.set_path('ForgeNotificationService')
  source = group.new_file('NotificationService.swift')
  info = group.new_file('Info.plist')
  entitlements = group.new_file('ForgeNotificationService.entitlements')
  target = project.new_target(:app_extension, 'ForgeNotificationService', platform, deployment)
  target.add_file_references([source])
  target.build_configurations.each do |config|
    settings = config.build_settings
    settings['PRODUCT_BUNDLE_IDENTIFIER'] = 'com.tingouw.forge.notification-service'
    settings['PRODUCT_NAME'] = '$(TARGET_NAME)'
    settings['INFOPLIST_FILE'] = 'ForgeNotificationService/Info.plist'
    settings['CODE_SIGN_ENTITLEMENTS'] = 'ForgeNotificationService/ForgeNotificationService.entitlements'
    settings['DEVELOPMENT_TEAM'] = 'QJ6C3M6J85'
    settings['CODE_SIGN_STYLE'] = 'Automatic'
    settings['SWIFT_VERSION'] = '5.0'
    settings['SKIP_INSTALL'] = 'YES'
    settings['GENERATE_INFOPLIST_FILE'] = 'NO'
    settings['APPLICATION_EXTENSION_API_ONLY'] = 'YES'
    if platform == :ios
      settings['IPHONEOS_DEPLOYMENT_TARGET'] = deployment
      settings['TARGETED_DEVICE_FAMILY'] = '1,2'
    else
      settings['MACOSX_DEPLOYMENT_TARGET'] = deployment
    end
  end
  # Embed Extensions/PlugIns and establish target dependency.
  embed_name = platform == :ios ? 'Embed Foundation Extensions' : 'Embed App Extensions'
  phase = app.copy_files_build_phases.find { |item| item.name == embed_name }
  unless phase
    phase = app.new_copy_files_build_phase(embed_name)
    phase.dst_subfolder_spec = platform == :ios ? '13' : '13'
  end
  build_file = phase.add_file_reference(target.product_reference, true)
  build_file.settings = { 'ATTRIBUTES' => ['RemoveHeadersOnCopy'] }
  app.add_dependency(target)
  project.save
end

add_extension(project_path: 'flutter/pi_go_app/ios/Runner.xcodeproj', platform: :ios, deployment: '13.0')

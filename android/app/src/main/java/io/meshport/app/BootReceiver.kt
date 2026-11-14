package io.meshport.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Starts the service after boot or an app update when autostart is enabled in settings. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        val prefs = context.getSharedPreferences(MeshApp.PREFS, Context.MODE_PRIVATE)
        if (prefs.getBoolean(MeshApp.PREF_AUTOSTART, false)) {
            MeshService.start(context)
        }
    }
}

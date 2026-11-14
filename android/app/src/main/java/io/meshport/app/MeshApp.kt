package io.meshport.app

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.os.Build

class MeshApp : Application() {
    override fun onCreate() {
        super.onCreate()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val nm = getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_SERVICE, getString(R.string.channel_service), NotificationManager.IMPORTANCE_MIN)
                    .apply { setShowBadge(false) }
            )
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_MESSAGES, getString(R.string.channel_messages), NotificationManager.IMPORTANCE_HIGH)
            )
        }
    }

    companion object {
        const val CHANNEL_SERVICE = "service"
        const val CHANNEL_MESSAGES = "messages"
        const val PREFS = "meshport"
        const val PREF_AUTOSTART = "autostart"
    }
}

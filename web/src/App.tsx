import { Navigate, Route, Routes } from 'react-router-dom'
import { AppLayout } from './components/AppLayout'
import { RequireAuth } from './components/RequireAuth'
import { ToastProvider } from './components/Toast'
import { AuthProvider } from './lib/auth'
import { PrefsProvider } from './lib/prefs'
import AuditPage from './pages/AuditPage'
import AlertsPage from './pages/AlertsPage'
import AppDetailPage from './pages/AppDetailPage'
import AppsPage from './pages/AppsPage'
import BackupsPage from './pages/BackupsPage'
import ChecksPage from './pages/ChecksPage'
import CredentialsPage from './pages/CredentialsPage'
import DashboardPage from './pages/DashboardPage'
import ImportPage from './pages/ImportPage'
import LoginPage from './pages/LoginPage'
import ReleasesPage from './pages/ReleasesPage'
import ReleaseDetailPage from './pages/ReleaseDetailPage'
import ScheduledTasksPage from './pages/ScheduledTasksPage'
import ServerDetailPage from './pages/ServerDetailPage'
import ServerToolsPage from './pages/ServerToolsPage'
import SettingsPage from './pages/SettingsPage'
import SetupPage from './pages/SetupPage'
import TokensPage from './pages/TokensPage'
import UsersPage from './pages/UsersPage'

export default function App() {
  return (
    <PrefsProvider>
      <ToastProvider>
        <AuthProvider>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route
              element={
                <RequireAuth>
                  <AppLayout />
                </RequireAuth>
              }
            >
              <Route index element={<DashboardPage />} />
              <Route path="/servers" element={<DashboardPage />} />
              <Route path="/servers/:id" element={<ServerDetailPage />} />
              <Route path="/checks" element={<ChecksPage />} />
              <Route path="/alerts" element={<AlertsPage />} />
              <Route path="/terminal" element={<ServerToolsPage kind="terminal" />} />
              <Route path="/files" element={<ServerToolsPage kind="files" />} />
              <Route path="/logs" element={<ServerToolsPage kind="logs" />} />
              <Route path="/scheduled-tasks" element={<ScheduledTasksPage />} />
              <Route path="/apps" element={<AppsPage />} />
              <Route path="/apps/:id" element={<AppDetailPage />} />
              <Route path="/releases" element={<ReleasesPage />} />
              <Route path="/releases/:id" element={<ReleaseDetailPage />} />
              <Route path="/credentials" element={<CredentialsPage />} />
              <Route path="/users" element={<UsersPage />} />
              <Route path="/tokens" element={<TokensPage />} />
              <Route path="/audit" element={<AuditPage />} />
              <Route path="/backups" element={<BackupsPage />} />
              <Route path="/import" element={<ImportPage />} />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="/setup" element={<SetupPage />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </AuthProvider>
      </ToastProvider>
    </PrefsProvider>
  )
}

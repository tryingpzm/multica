/** Settings tab shell routing between the stage settings: Sources, Eval and Execution. */
import { EvalSettings } from './eval'
import { ExecutionSettings } from './execution'
import { usePanel } from './provider'
import { SETTINGS_GROUPS, settingsRoute, type SettingsGroup } from './route'
import { SettingsProvider } from './settings-kit'
import { SourceSettings } from './source'

export function SettingsPage({ group, query }: { group: SettingsGroup; query: { source?: string; version?: string } }) {
  const { t, navigate } = usePanel()
  const content = (() => {
    switch (group) {
      case 'source': return <SettingsProvider><SourceSettings {...query.source ? { focus: query.source } : {}} /></SettingsProvider>
      case 'eval': return <SettingsProvider><EvalSettings /></SettingsProvider>
      case 'execution': return <SettingsProvider><ExecutionSettings /></SettingsProvider>
    }
  })()
  return <div className="dt-settings">
    <nav className="dt-settings-nav" aria-label={t('tab.settings')}>
      {SETTINGS_GROUPS.map(value => <button key={value} type="button" className="dt-settings-link" data-stage
        aria-current={value === group ? 'page' : undefined} onClick={() => navigate(settingsRoute(value),
          true)}>{t(`settingsGroup.${value}`)}</button>)}
    </nav>
    <div className="dt-settings-body">
      <h2 className="dt-settings-title">{t(`settingsGroup.${group}`)}</h2>
      {content}
    </div>
  </div>
}

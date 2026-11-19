import { ScreenLayout } from '../components/shell/ScreenLayout.tsx'

/** Placeholder until this screen's own PR lands (tracking: #163). */
export function KeysScreen() {
  return (
    <ScreenLayout
      sideLabel="Keys & usage"
      side={null}
      main={<h1 style={{ padding: 24, fontSize: 17 }}>Keys & usage</h1>}
      inspectorLabel="Details"
      inspector={null}
    />
  )
}

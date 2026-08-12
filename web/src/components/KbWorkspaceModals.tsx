import { lazy, Suspense } from 'react';
import { Loader2 } from 'lucide-react';
import { useKbCore } from '../contexts/KbCoreContext';
import { useKbChat } from '../contexts/KbChatContext';
import { useKbData } from '../contexts/KbDataContext';

const MembersModal = lazy(() => import('./MembersModal').then(module => ({ default: module.MembersModal })));
const SettingsModal = lazy(() => import('./SettingsModal').then(module => ({ default: module.SettingsModal })));
const ChartModal = lazy(() => import('./ChartModal').then(module => ({ default: module.ChartModal })));
const AbstractGenerationModal = lazy(() => import('./AbstractGenerationModal').then(module => ({ default: module.AbstractGenerationModal })));
const ContentModal = lazy(() => import('./ContentModal').then(module => ({ default: module.ContentModal })));
const PreviewModal = lazy(() => import('./PreviewModal').then(module => ({ default: module.PreviewModal })));
const PdfPreviewModal = lazy(() => import('./PdfPreviewModal').then(module => ({ default: module.PdfPreviewModal })));
const WebWorkspace = lazy(() => import('./WebWorkspace').then(module => ({ default: module.WebWorkspace })));

export function KbWorkspaceModals() {
  const { currentKb, availableConfigs, handleUpdateKBSettings } = useKbCore();
  const { showSettings, setShowSettings } = useKbChat();
  const { sharing, content, fileMgmt, webTools } = useKbData();

  return (
    <>
      <Suspense fallback={null}>
        {sharing.showShareModal && <MembersModal
          show={sharing.showShareModal}
          onClose={() => sharing.setShowShareModal(false)}
          sharingKb={sharing.sharingKb}
          shareUserId={sharing.shareUserId}
          setShareUserId={sharing.setShareUserId}
          shareTargetUser={sharing.shareTargetUser}
          shareLoading={sharing.shareLoading}
          sharePermission={sharing.sharePermission}
          setSharePermission={sharing.setSharePermission}
          onLookupUser={sharing.lookupUser}
          onConfirmShare={sharing.confirmShare}
          notFoundUsername={sharing.notFoundUsername}
          onPendingInvited={sharing.clearNotFound}
          myRole={currentKb?.myRole ?? 'view'}
        />}
      </Suspense>

      <Suspense fallback={null}>
        {showSettings && <SettingsModal
          show={showSettings}
          onClose={() => setShowSettings(false)}
          currentKb={currentKb}
          availableConfigs={availableConfigs}
          onUpdateSettings={handleUpdateKBSettings}
        />}
      </Suspense>

      <Suspense fallback={null}>
        {content.showChartGenerationModal && <ChartModal
          show={content.showChartGenerationModal}
          onClose={() => content.setShowChartGenerationModal(false)}
          chartPrompt={content.chartPrompt}
          setChartPrompt={content.setChartPrompt}
          selectedFileId={content.selectedFileId}
          setSelectedFileId={content.setSelectedFileId}
          files={fileMgmt.files}
          onSubmit={content.submitChartGeneration}
        />}
      </Suspense>

      <Suspense fallback={null}>
        {content.showAbstractModal && <AbstractGenerationModal
          show={content.showAbstractModal}
          onClose={() => content.setShowAbstractModal(false)}
          abstractFileId={content.abstractFileId}
          setAbstractFileId={content.setAbstractFileId}
          abstractType={content.abstractType}
          setAbstractType={content.setAbstractType}
          files={fileMgmt.files}
          onSubmit={content.submitAbstractGeneration}
        />}
      </Suspense>

      <Suspense fallback={null}>
        {content.showContentModal && <ContentModal
          show={content.showContentModal}
          onClose={() => content.setShowContentModal(false)}
          selectedContent={content.selectedContent}
          currentCardIndex={content.currentCardIndex}
          setCurrentCardIndex={content.setCurrentCardIndex}
          isAnswerVisible={content.isAnswerVisible}
          setIsAnswerVisible={content.setIsAnswerVisible}
        />}
      </Suspense>

      <Suspense fallback={null}>
        {webTools.showPreviewModal && <PreviewModal
          show={webTools.showPreviewModal}
          onClose={() => webTools.setShowPreviewModal(false)}
          previewPage={webTools.previewPage}
        />}
      </Suspense>

      {webTools.showWebWorkspace && currentKb && (
        <Suspense fallback={<div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50"><Loader2 className="animate-spin text-white" /></div>}>
          <WebWorkspace
            type={webTools.toolTab === 'websearch' ? 'websearch' : 'crawl'}
            results={(webTools.toolTab === 'websearch' ? webTools.searchResults.map(r => ({ ...r, content: r.content ?? '' })) : webTools.crawlResults)}
            selectedUrls={webTools.selectedPages}
            onToggleSelection={(url) => {
              const next = new Set(webTools.selectedPages);
              if (next.has(url)) next.delete(url);
              else next.add(url);
              webTools.setSelectedPages(next);
            }}
            onToggleSelectAll={() => {
              const list = webTools.toolTab === 'websearch' ? webTools.searchResults : webTools.crawlResults;
              if (webTools.selectedPages.size === list.length) {
                webTools.setSelectedPages(new Set());
              } else {
                webTools.setSelectedPages(new Set(list.map(r => r.url)));
              }
            }}
            onAddSources={webTools.handleAddSources}
            onClose={() => webTools.setShowWebWorkspace(false)}
            onUpdateResult={webTools.handleUpdateWebResult}
            onStartEdit={webTools.handleStartEditWebResult}
          />
        </Suspense>
      )}

      <Suspense fallback={null}>
        {webTools.showPdfPreview && <PdfPreviewModal
          show={webTools.showPdfPreview}
          onClose={() => webTools.setShowPdfPreview(false)}
          fileId={webTools.pdfPreview?.fileId || ''}
          fileName={webTools.pdfPreview?.fileName || ''}
          page={webTools.pdfPreview?.page || 1}
        />}
      </Suspense>
    </>
  );
}
